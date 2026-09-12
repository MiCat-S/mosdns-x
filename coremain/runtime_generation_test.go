package coremain

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	D "github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

type runtimeTestHandler struct {
	answer  byte
	entered chan struct{}
	release <-chan struct{}
}

type runtimeBackgroundHandler struct {
	started chan struct{}
	release <-chan struct{}
	done    chan struct{}
}

func (h runtimeBackgroundHandler) ServeDNS(_ context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	releaseWork, ok := meta.AcquireBackgroundWork()
	if !ok {
		return nil, errors.New("background work rejected")
	}
	go func() {
		defer close(h.done)
		defer releaseWork()
		close(h.started)
		<-h.release
	}()
	response := new(dns.Msg)
	response.SetReply(req)
	return response, nil
}

func (h runtimeTestHandler) ServeDNS(ctx context.Context, req *dns.Msg, _ *query_context.RequestMeta) (*dns.Msg, error) {
	if h.entered != nil {
		close(h.entered)
	}
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	response := new(dns.Msg)
	response.SetReply(req)
	response.Answer = append(response.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   []byte{192, 0, 2, h.answer},
	})
	return response, nil
}

func runtimeTestGeneration(handler D.Handler, closeResources func() error) *RuntimeGeneration {
	return newRuntimeGeneration([][]D.Handler{{handler}}, handler, http.NotFoundHandler(), prometheus.NewRegistry(), closeResources)
}

func stageRuntimeTestGeneration(t *testing.T, manager *RuntimeManager, generation *RuntimeGeneration) *RuntimeStage {
	t.Helper()
	stage, err := manager.Stage(context.Background(), func(context.Context) (*RuntimeGeneration, error) {
		return generation, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

func runtimeTestQuery(handler D.Handler) (byte, error) {
	request := new(dns.Msg).SetQuestion("runtime.test.", dns.TypeA)
	response, err := handler.ServeDNS(context.Background(), request, query_context.NewRequestMeta(netip.Addr{}))
	if err != nil {
		return 0, err
	}
	answer, ok := response.Answer[0].(*dns.A)
	if !ok || len(answer.A) != 4 {
		return 0, errors.New("unexpected runtime test answer")
	}
	return answer.A[3], nil
}

func TestRuntimeManagerSwapRoutesNewRequestsAndDrainsOld(t *testing.T) {
	manager := NewRuntimeManager()
	releaseOld := make(chan struct{})
	enteredOld := make(chan struct{})
	oldClosed := make(chan struct{})
	old := runtimeTestGeneration(runtimeTestHandler{answer: 1, entered: enteredOld, release: releaseOld}, func() error {
		close(oldClosed)
		return nil
	})
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, old)); err != nil {
		t.Fatal(err)
	}

	stableHandler := manager.DNSHandler(0, 0)
	type queryResult struct {
		answer byte
		err    error
	}
	requestDone := make(chan queryResult, 1)
	go func() {
		answer, err := runtimeTestQuery(stableHandler)
		requestDone <- queryResult{answer: answer, err: err}
	}()
	<-enteredOld

	newGeneration := runtimeTestGeneration(runtimeTestHandler{answer: 2}, nil)
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, newGeneration)); err != nil {
		t.Fatal(err)
	}
	got, err := runtimeTestQuery(stableHandler)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("new request used generation %d, want 2", got)
	}
	select {
	case <-oldClosed:
		t.Fatal("old generation closed while a request was in flight")
	default:
	}

	_, err = manager.Stage(context.Background(), func(context.Context) (*RuntimeGeneration, error) {
		return runtimeTestGeneration(runtimeTestHandler{answer: 3}, nil), nil
	})
	if !errors.Is(err, ErrRuntimePreviousGenerationAlive) {
		t.Fatalf("Stage error = %v, want %v", err, ErrRuntimePreviousGenerationAlive)
	}

	close(releaseOld)
	result := <-requestDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.answer != 1 {
		t.Fatalf("in-flight request used generation %d, want 1", result.answer)
	}
	if err := manager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldClosed:
	default:
		t.Fatal("old generation resources were not released")
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDrainWaitsForRequestBackgroundWork(t *testing.T) {
	manager := NewRuntimeManager()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	closed := make(chan struct{})
	old := runtimeTestGeneration(runtimeBackgroundHandler{started: started, release: release, done: done}, func() error {
		close(closed)
		return nil
	})
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, old)); err != nil {
		t.Fatal(err)
	}
	request := new(dns.Msg).SetQuestion("runtime.test.", dns.TypeA)
	if _, err := manager.DNSHandler(0, 0).ServeDNS(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, runtimeTestGeneration(runtimeTestHandler{answer: 2}, nil))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
		t.Fatal("old generation closed while request background work was running")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-done
	if err := manager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("old generation did not close after request background work finished")
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeStageFailureDrainsCandidateAndReleasesReloadSlot(t *testing.T) {
	manager := NewRuntimeManager()
	var closed atomic.Int32
	wantErr := errors.New("candidate failed")
	candidate := runtimeTestGeneration(runtimeTestHandler{answer: 1}, func() error {
		closed.Add(1)
		return nil
	})
	stage, err := manager.Stage(context.Background(), func(context.Context) (*RuntimeGeneration, error) {
		return candidate, wantErr
	})
	if stage != nil || !errors.Is(err, wantErr) {
		t.Fatalf("Stage = (%v, %v), want nil and build error", stage, err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("candidate closed %d times, want 1", got)
	}

	valid := runtimeTestGeneration(runtimeTestHandler{answer: 2}, nil)
	stage, err = manager.Stage(context.Background(), func(context.Context) (*RuntimeGeneration, error) {
		return valid, nil
	})
	if err != nil {
		t.Fatalf("reload slot remained locked: %v", err)
	}
	if err := manager.Discard(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
}

type runtimeBuildPlugin struct {
	*BP
	closed *atomic.Int32
}

func (p *runtimeBuildPlugin) Exec(context.Context, *query_context.Context, executable_seq.ExecutableChainNode) error {
	return nil
}

func (p *runtimeBuildPlugin) Close() error {
	p.closed.Add(1)
	return nil
}

func TestRuntimeBuildFailureClosesInitializedPlugins(t *testing.T) {
	const pluginType = "runtime_generation_test_closer"
	var closed atomic.Int32
	RegNewPluginFunc(pluginType, func(bp *BP, _ interface{}) (Plugin, error) {
		return &runtimeBuildPlugin{BP: bp, closed: &closed}, nil
	}, nil)
	defer DelPluginType(pluginType)

	owner := &Mosdns{logger: zap.NewNop()}
	manager := NewRuntimeManager()
	stage, err := manager.Stage(context.Background(), func(ctx context.Context) (*RuntimeGeneration, error) {
		return owner.buildRuntimeGeneration(ctx, &Config{Plugins: []PluginConfig{
			{Tag: "initialized", Type: pluginType},
			{Tag: "broken", Type: "runtime_generation_test_missing"},
		}})
	})
	if stage != nil || err == nil {
		t.Fatalf("Stage = (%v, %v), want initialization error", stage, err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("initialized plugin closed %d times, want 1", got)
	}
}

func TestRuntimeStageRejectsListenerTopologyChange(t *testing.T) {
	manager := NewRuntimeManager()
	initial := runtimeTestGeneration(runtimeTestHandler{answer: 1}, nil)
	initial.topology[0][0].addr = "127.0.0.1:53"
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, initial)); err != nil {
		t.Fatal(err)
	}
	var candidateClosed atomic.Bool
	candidate := runtimeTestGeneration(runtimeTestHandler{answer: 2}, func() error {
		candidateClosed.Store(true)
		return nil
	})
	candidate.topology[0][0].addr = "127.0.0.1:54"
	stage, err := manager.Stage(context.Background(), func(context.Context) (*RuntimeGeneration, error) {
		return candidate, nil
	})
	if stage != nil || !errors.Is(err, ErrRuntimeTopologyChanged) {
		t.Fatalf("Stage = (%v, %v), want topology error", stage, err)
	}
	if !candidateClosed.Load() {
		t.Fatal("rejected topology candidate was not released")
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDrainWaitsForStagedReload(t *testing.T) {
	manager := NewRuntimeManager()
	currentClosed := make(chan struct{})
	current := runtimeTestGeneration(runtimeTestHandler{answer: 1}, func() error {
		close(currentClosed)
		return nil
	})
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, current)); err != nil {
		t.Fatal(err)
	}

	candidateClosed := make(chan struct{})
	candidate := runtimeTestGeneration(runtimeTestHandler{answer: 2}, func() error {
		close(candidateClosed)
		return nil
	})
	stage := stageRuntimeTestGeneration(t, manager, candidate)
	drainDone := make(chan error, 1)
	go func() { drainDone <- manager.Drain(context.Background()) }()
	select {
	case err := <-drainDone:
		t.Fatalf("Drain returned before staged reload completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := manager.Discard(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-candidateClosed:
	default:
		t.Fatal("discarded candidate was not closed")
	}
	select {
	case <-currentClosed:
	default:
		t.Fatal("current generation was not closed by Drain")
	}
}

func TestRuntimeDrainReloadWaitHonorsContext(t *testing.T) {
	manager := NewRuntimeManager()
	stage := stageRuntimeTestGeneration(t, manager, runtimeTestGeneration(runtimeTestHandler{answer: 1}, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain error = %v, want deadline exceeded", err)
	}
	if err := manager.Discard(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type runtimeBlockingExecutable struct {
	entered chan struct{}
	release <-chan struct{}
}

func (e runtimeBlockingExecutable) Exec(ctx context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
	close(e.entered)
	select {
	case <-e.release:
		qCtx.SetResponse(new(dns.Msg))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type runtimeDetachedExecutable struct {
	child executable_seq.Executable
	done  chan<- error
}

func (e runtimeDetachedExecutable) Exec(_ context.Context, qCtx *query_context.Context, _ executable_seq.ExecutableChainNode) error {
	childCtx := qCtx.Copy()
	go func() {
		e.done <- e.child.Exec(context.Background(), childCtx, nil)
	}()
	qCtx.SetResponse(new(dns.Msg))
	return nil
}

type runtimeExecutableHandler struct{ executable executable_seq.Executable }

func (h runtimeExecutableHandler) ServeDNS(ctx context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	qCtx := query_context.NewContext(req, meta)
	if err := h.executable.Exec(ctx, qCtx, nil); err != nil {
		return nil, err
	}
	return qCtx.R(), nil
}

func TestRuntimeDrainWaitsForDetachedExecutableBranch(t *testing.T) {
	manager := NewRuntimeManager()
	releaseBranch := make(chan struct{})
	branchEntered := make(chan struct{})
	branchDone := make(chan error, 1)
	closed := make(chan struct{})
	generation := newRuntimeGeneration(nil, nil, nil, nil, func() error {
		close(closed)
		return nil
	})
	child := generation.wrapExecutable(runtimeBlockingExecutable{entered: branchEntered, release: releaseBranch})
	root := generation.wrapExecutable(runtimeDetachedExecutable{child: child, done: branchDone})
	handler := runtimeExecutableHandler{executable: root}
	generation.entries = [][]D.Handler{{handler}}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, generation)); err != nil {
		t.Fatal(err)
	}

	request := new(dns.Msg).SetQuestion("branch.test.", dns.TypeA)
	if _, err := manager.DNSHandler(0, 0).ServeDNS(context.Background(), request, query_context.NewRequestMeta(netip.Addr{})); err != nil {
		t.Fatal(err)
	}
	<-branchEntered
	drainDone := make(chan error, 1)
	go func() { drainDone <- manager.Drain(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("generation closed while detached branch was executing")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseBranch)
	if err := <-branchDone; err != nil {
		t.Fatal(err)
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("generation did not close after detached branch completed")
	}
}

func TestRuntimeMetricsAllowSameCollectorsAcrossGenerations(t *testing.T) {
	manager := NewRuntimeManager()
	makeGeneration := func(value float64) *RuntimeGeneration {
		registry := prometheus.NewRegistry()
		gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "runtime_generation_value"})
		gauge.Set(value)
		registry.MustRegister(gauge)
		return newRuntimeGeneration([][]D.Handler{{runtimeTestHandler{answer: byte(value)}}}, nil, nil, registry, nil)
	}

	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, makeGeneration(1))); err != nil {
		t.Fatal(err)
	}
	families, err := manager.Gather()
	if err != nil || len(families) != 1 || families[0].Metric[0].GetGauge().GetValue() != 1 {
		t.Fatalf("first Gather = (%v, %v)", families, err)
	}
	if err := manager.Swap(stageRuntimeTestGeneration(t, manager, makeGeneration(2))); err != nil {
		t.Fatal(err)
	}
	families, err = manager.Gather()
	if err != nil || len(families) != 1 || families[0].Metric[0].GetGauge().GetValue() != 2 {
		t.Fatalf("second Gather = (%v, %v)", families, err)
	}
	if err := manager.WaitForPreviousDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type runtimeClosePlugin struct {
	tag   string
	order *[]string
	mu    *sync.Mutex
}

func (p runtimeClosePlugin) Tag() string  { return p.tag }
func (p runtimeClosePlugin) Type() string { return "test" }
func (p runtimeClosePlugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.order = append(*p.order, p.tag)
	return nil
}

func TestRuntimeResourcesClosePluginsInReverseOrder(t *testing.T) {
	var order []string
	var mu sync.Mutex
	owner := &Mosdns{plugins: []Plugin{
		runtimeClosePlugin{tag: "first", order: &order, mu: &mu},
		runtimeClosePlugin{tag: "second", order: &order, mu: &mu},
	}}
	if err := owner.shutdownRuntimeResources(); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("close order = %v, want [second first]", order)
	}
}
