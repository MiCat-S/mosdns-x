package coremain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"

	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

var (
	ErrRuntimeReloadInProgress        = errors.New("runtime reload is already in progress")
	ErrRuntimePreviousGenerationAlive = errors.New("previous runtime generation is still draining")
	ErrRuntimeManagerClosed           = errors.New("runtime manager is closed")
	ErrRuntimeStageConsumed           = errors.New("runtime stage has already been consumed")
	ErrRuntimeStageOwner              = errors.New("runtime stage belongs to another manager")
	ErrRuntimeTopologyChanged         = errors.New("runtime listener topology changed")
	ErrRuntimeGenerationDraining      = errors.New("runtime generation is draining")
)

// RuntimeBuildFunc builds a complete, inactive runtime generation. If it
// returns both a generation and an error, Stage drains that generation before
// returning so partially initialized resources are not leaked.
type RuntimeBuildFunc func(context.Context) (*RuntimeGeneration, error)

// RuntimeGeneration owns one replaceable DNS runtime: its entry handlers,
// plugin HTTP handlers, metrics registry, and the resources behind them.
// Generations are activated only by RuntimeManager.Swap.
type RuntimeGeneration struct {
	entries        [][]D.Handler
	topology       [][]runtimeListenerIdentity
	lookup         D.Handler
	pluginHandler  http.Handler
	metrics        prometheus.Gatherer
	closeResources func() error

	requestMu         sync.Mutex
	acceptingRequests bool
	draining          bool
	requestWG         sync.WaitGroup

	workMu        sync.Mutex
	acceptingWork bool
	workWG        sync.WaitGroup

	drainOnce sync.Once
	drainDone chan struct{}
	drainErr  error
}

func newRuntimeGeneration(
	entries [][]D.Handler,
	lookup D.Handler,
	pluginHandler http.Handler,
	metrics prometheus.Gatherer,
	closeResources func() error,
) *RuntimeGeneration {
	topology := make([][]runtimeListenerIdentity, len(entries))
	for i := range entries {
		topology[i] = make([]runtimeListenerIdentity, len(entries[i]))
	}
	return &RuntimeGeneration{
		entries:        entries,
		topology:       topology,
		lookup:         lookup,
		pluginHandler:  pluginHandler,
		metrics:        metrics,
		closeResources: closeResources,
		acceptingWork:  true,
		drainDone:      make(chan struct{}),
	}
}

func (g *RuntimeGeneration) activate() error {
	g.requestMu.Lock()
	defer g.requestMu.Unlock()
	if g.draining {
		return ErrRuntimeGenerationDraining
	}
	g.acceptingRequests = true
	return nil
}

func (g *RuntimeGeneration) stopRequests() {
	g.requestMu.Lock()
	g.acceptingRequests = false
	g.requestMu.Unlock()
}

func (g *RuntimeGeneration) acquireRequest() bool {
	g.requestMu.Lock()
	defer g.requestMu.Unlock()
	if !g.acceptingRequests {
		return false
	}
	g.requestWG.Add(1)
	return true
}

func (g *RuntimeGeneration) releaseRequest() {
	g.requestWG.Done()
}

func (g *RuntimeGeneration) acquireWork() bool {
	g.workMu.Lock()
	defer g.workMu.Unlock()
	if !g.acceptingWork {
		return false
	}
	g.workWG.Add(1)
	return true
}

func (g *RuntimeGeneration) releaseWork() {
	g.workWG.Done()
}

func (g *RuntimeGeneration) acquireBackgroundWork() (func(), bool) {
	if !g.acquireWork() {
		return nil, false
	}
	return g.releaseWork, true
}

func (g *RuntimeGeneration) beginDrain() {
	g.drainOnce.Do(func() {
		g.requestMu.Lock()
		g.acceptingRequests = false
		g.draining = true
		g.requestMu.Unlock()
		go func() {
			// A request lease covers the complete entry call. Once all entry
			// calls have returned, no synchronous path can start more work.
			g.requestWG.Wait()

			// Executable and matcher wrappers also cover parallel/fallback
			// branches that can outlive the entry call. Closing this gate keeps
			// a late branch from entering a plugin after resources are closed.
			g.workMu.Lock()
			g.acceptingWork = false
			g.workMu.Unlock()
			g.workWG.Wait()

			if g.closeResources != nil {
				g.drainErr = g.closeResources()
			}
			close(g.drainDone)
		}()
	})
}

// Drain stops the generation from accepting requests, waits for in-flight
// entry and branch work, then releases its resources. If ctx expires, draining
// continues in the background and a later call can wait for the same result.
func (g *RuntimeGeneration) Drain(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.beginDrain()
	select {
	case <-g.drainDone:
		return g.drainErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *RuntimeGeneration) handler(serverIndex, listenerIndex int) D.Handler {
	if serverIndex < 0 || serverIndex >= len(g.entries) {
		return nil
	}
	listeners := g.entries[serverIndex]
	if listenerIndex < 0 || listenerIndex >= len(listeners) {
		return nil
	}
	return listeners[listenerIndex]
}

func (g *RuntimeGeneration) sameTopology(other *RuntimeGeneration) bool {
	if other == nil || len(g.topology) != len(other.topology) {
		return false
	}
	for i := range g.topology {
		if len(g.topology[i]) != len(other.topology[i]) {
			return false
		}
		for j := range g.topology[i] {
			if g.topology[i][j] != other.topology[i][j] {
				return false
			}
		}
	}
	return true
}

func (g *RuntimeGeneration) wrapExecutable(exec executable_seq.Executable) executable_seq.Executable {
	return trackedExecutable{generation: g, executable: exec}
}

func (g *RuntimeGeneration) wrapMatcher(matcher executable_seq.Matcher) executable_seq.Matcher {
	return trackedMatcher{generation: g, matcher: matcher}
}

type trackedExecutable struct {
	generation *RuntimeGeneration
	executable executable_seq.Executable
}

func (e trackedExecutable) Exec(ctx context.Context, qCtx *query_context.Context, next executable_seq.ExecutableChainNode) error {
	if !e.generation.acquireWork() {
		return ErrRuntimeGenerationDraining
	}
	defer e.generation.releaseWork()
	return e.executable.Exec(ctx, qCtx, next)
}

type trackedMatcher struct {
	generation *RuntimeGeneration
	matcher    executable_seq.Matcher
}

func (m trackedMatcher) Match(ctx context.Context, qCtx *query_context.Context) (bool, error) {
	if !m.generation.acquireWork() {
		return false, ErrRuntimeGenerationDraining
	}
	defer m.generation.releaseWork()
	return m.matcher.Match(ctx, qCtx)
}

// RuntimeStage reserves the manager's reload slot until it is swapped or
// discarded. This makes stage and swap one serialized reload transaction.
type RuntimeStage struct {
	manager    *RuntimeManager
	generation *RuntimeGeneration
	consumed   atomic.Bool
}

// RuntimeManager atomically routes stable listeners to the current runtime.
type RuntimeManager struct {
	current atomic.Pointer[RuntimeGeneration]
	reload  chan struct{}

	mu           sync.Mutex
	draining     *RuntimeGeneration
	lastDrainErr error
	closed       bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

func NewRuntimeManager() *RuntimeManager {
	return &RuntimeManager{reload: make(chan struct{}, 1), shutdownDone: make(chan struct{})}
}

func (m *RuntimeManager) beginReload() error {
	select {
	case m.reload <- struct{}{}:
	default:
		return ErrRuntimeReloadInProgress
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		<-m.reload
		return ErrRuntimeManagerClosed
	}
	if m.draining != nil {
		<-m.reload
		return ErrRuntimePreviousGenerationAlive
	}
	return nil
}

func (m *RuntimeManager) endReload() {
	<-m.reload
}

// Stage serializes a candidate build with swap and validates that a reload
// keeps the listener topology used by stable servers.
func (m *RuntimeManager) Stage(ctx context.Context, build RuntimeBuildFunc) (*RuntimeStage, error) {
	if build == nil {
		return nil, errors.New("nil runtime build function")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.beginReload(); err != nil {
		return nil, err
	}
	releaseReload := true
	defer func() {
		if releaseReload {
			m.endReload()
		}
	}()

	candidate, err := callRuntimeBuild(ctx, build)
	if err != nil {
		if candidate != nil {
			err = errors.Join(err, candidate.Drain(context.Background()))
		}
		return nil, err
	}
	if candidate == nil {
		return nil, errors.New("runtime build returned a nil generation")
	}
	if current := m.current.Load(); current != nil && !current.sameTopology(candidate) {
		return nil, errors.Join(ErrRuntimeTopologyChanged, candidate.Drain(context.Background()))
	}

	releaseReload = false
	return &RuntimeStage{manager: m, generation: candidate}, nil
}

func callRuntimeBuild(ctx context.Context, build RuntimeBuildFunc) (generation *RuntimeGeneration, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runtime build panicked: %v", recovered)
		}
	}()
	return build(ctx)
}

// Swap atomically publishes a staged generation. The old generation drains in
// the background; another Stage is rejected until it has been fully released.
func (m *RuntimeManager) Swap(stage *RuntimeStage) error {
	if stage == nil || stage.manager != m {
		return ErrRuntimeStageOwner
	}
	if !stage.consumed.CompareAndSwap(false, true) {
		return ErrRuntimeStageConsumed
	}
	defer m.endReload()

	candidate := stage.generation
	if err := candidate.activate(); err != nil {
		return errors.Join(err, candidate.Drain(context.Background()))
	}
	previous := m.current.Swap(candidate)
	if previous == nil {
		return nil
	}
	previous.stopRequests()

	m.mu.Lock()
	m.draining = previous
	m.mu.Unlock()
	previous.beginDrain()
	go func() {
		err := previous.Drain(context.Background())
		m.finishPreviousDrain(previous, err)
	}()
	return nil
}

func (m *RuntimeManager) finishPreviousDrain(generation *RuntimeGeneration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.draining != generation {
		return
	}
	m.draining = nil
	m.lastDrainErr = errors.Join(m.lastDrainErr, err)
}

// Discard releases a staged candidate without publishing it.
func (m *RuntimeManager) Discard(ctx context.Context, stage *RuntimeStage) error {
	if stage == nil || stage.manager != m {
		return ErrRuntimeStageOwner
	}
	if !stage.consumed.CompareAndSwap(false, true) {
		return ErrRuntimeStageConsumed
	}
	defer m.endReload()
	return stage.generation.Drain(ctx)
}

// WaitForPreviousDrain waits for the generation replaced by the latest swap.
func (m *RuntimeManager) WaitForPreviousDrain(ctx context.Context) error {
	m.mu.Lock()
	draining := m.draining
	lastErr := m.lastDrainErr
	m.mu.Unlock()
	if draining == nil {
		return lastErr
	}
	if err := draining.Drain(ctx); err != nil {
		return err
	}
	m.finishPreviousDrain(draining, draining.drainErr)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastDrainErr
}

// Drain permanently stops routing and releases the current and any previous
// generation. Stable listeners should be stopped before calling Drain.
func (m *RuntimeManager) Drain(ctx context.Context) error {
	select {
	case m.reload <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.endReload()
		return m.waitForShutdown(ctx)
	}
	m.closed = true
	previous := m.draining
	m.mu.Unlock()
	current := m.current.Swap(nil)
	m.endReload()
	if current != nil {
		current.beginDrain()
	}
	m.shutdownOnce.Do(func() {
		go func() {
			if previous != nil && previous != current {
				m.finishPreviousDrain(previous, previous.Drain(context.Background()))
			}
			var currentErr error
			if current != nil {
				currentErr = current.Drain(context.Background())
			}
			m.mu.Lock()
			m.shutdownErr = errors.Join(m.lastDrainErr, currentErr)
			m.mu.Unlock()
			close(m.shutdownDone)
		}()
	})
	return m.waitForShutdown(ctx)
}

func (m *RuntimeManager) waitForShutdown(ctx context.Context) error {
	select {
	case <-m.shutdownDone:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *RuntimeManager) acquireCurrent() (*RuntimeGeneration, bool) {
	for {
		generation := m.current.Load()
		if generation == nil {
			return nil, false
		}
		if !generation.acquireRequest() {
			runtime.Gosched()
			continue
		}
		// If a swap happened between Load and acquireRequest, release the old
		// lease and retry so the request cannot enter a retired generation.
		if m.current.Load() == generation {
			return generation, true
		}
		generation.releaseRequest()
	}
}

// DNSHandler returns a stable handler for one configured listener.
func (m *RuntimeManager) DNSHandler(serverIndex, listenerIndex int) D.Handler {
	return runtimeDNSHandler{manager: m, serverIndex: serverIndex, listenerIndex: listenerIndex}
}

type runtimeDNSHandler struct {
	manager       *RuntimeManager
	serverIndex   int
	listenerIndex int
}

func (h runtimeDNSHandler) ServeDNS(ctx context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	generation, ok := h.manager.acquireCurrent()
	if !ok {
		return servfail(req), nil
	}
	defer generation.releaseRequest()
	meta = meta.Copy()
	meta.SetBackgroundWorkTracker(generation.acquireBackgroundWork)
	handler := generation.handler(h.serverIndex, h.listenerIndex)
	if handler == nil {
		return servfail(req), nil
	}
	return handler.ServeDNS(ctx, req, meta)
}

// LookupHandler returns a stable handler for panel lookups.
func (m *RuntimeManager) LookupHandler() D.Handler {
	return runtimeLookupHandler{manager: m}
}

type runtimeLookupHandler struct{ manager *RuntimeManager }

func (h runtimeLookupHandler) ServeDNS(ctx context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	generation, ok := h.manager.acquireCurrent()
	if !ok {
		return servfail(req), nil
	}
	defer generation.releaseRequest()
	meta = meta.Copy()
	meta.SetBackgroundWorkTracker(generation.acquireBackgroundWork)
	if generation.lookup == nil {
		return servfail(req), nil
	}
	return generation.lookup.ServeDNS(ctx, req, meta)
}

func servfail(req *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(req)
	response.Rcode = dns.RcodeServerFailure
	return response
}

// PluginHandler routes stable API requests to the current plugin mux.
func (m *RuntimeManager) PluginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generation, ok := m.acquireCurrent()
		if !ok {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		defer generation.releaseRequest()
		if generation.pluginHandler == nil {
			http.NotFound(w, r)
			return
		}
		generation.pluginHandler.ServeHTTP(w, r)
	})
}

// Gather implements prometheus.Gatherer and exposes only the current
// generation, allowing every candidate to register the same metric names.
func (m *RuntimeManager) Gather() ([]*io_prometheus_client.MetricFamily, error) {
	generation, ok := m.acquireCurrent()
	if !ok {
		return nil, nil
	}
	defer generation.releaseRequest()
	if generation.metrics == nil {
		return nil, nil
	}
	return generation.metrics.Gather()
}
