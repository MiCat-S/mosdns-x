/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package dns_handler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/dnsutils"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/query_access"
	"github.com/pmkol/mosdns-x/pkg/utils"
)

const (
	defaultQueryTimeout = time.Second * 5
)

var nopLogger = zap.NewNop()

// Handler handles dns query.
type Handler interface {
	// ServeDNS handles incoming request req and returns a response.
	// Implements must not keep and use req after the ServeDNS returned.
	// ServeDNS should handle dns errors by itself and return a proper error responses
	// for clients.
	// ServeDNS should always return a responses.
	// If ServeDNS returns an error, caller considers that the error is associated
	// with the downstream connection and will close the downstream connection
	// immediately.
	// All input parameters won't be nil.
	ServeDNS(ctx context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error)
}

type EntryHandlerOpts struct {
	// Logger is used for logging. Default is a noop logger.
	Logger *zap.Logger

	Entry executable_seq.Executable

	// QueryTimeout limits the timeout value of each query.
	// Default is defaultQueryTimeout.
	QueryTimeout time.Duration

	// RecursionAvailable sets the dns.Msg.RecursionAvailable flag globally.
	RecursionAvailable bool

	// Admit authorizes an authenticated principal after DNS question validation.
	Admit func(context.Context, query_context.Principal) error

	// Observe receives one self-contained result for every request.
	Observe func(Result)

	// CaptureQueryDetails snapshots EDNS metadata and response addresses for
	// query logging. Leave it disabled when only aggregate telemetry is used.
	CaptureQueryDetails bool
}

// Result is an immutable snapshot of a completed entry request.
type Result struct {
	Principal            query_context.Principal
	Protocol             string
	ClientAddr           netip.Addr
	AnswerIPs            []string
	EDNS                 EDNSInfo
	QuestionName         string
	QuestionType         uint16
	Duration             time.Duration
	Rcode                int
	ExecError            bool
	Admitted             bool
	Rejected             bool
	AccessKind           query_access.Kind
	CacheHit             bool
	ResponseSource       string
	ResponseSourceID     string
	UpstreamID           string
	EDNSTraceVersion     uint8
	UpstreamStageStatus  string
	UpstreamRequestEDNS  *dnsutils.EDNSSnapshot
	UpstreamResponseEDNS *dnsutils.EDNSSnapshot
	ResponseEDNS         *dnsutils.EDNSSnapshot
}

type EDNSInfo = dnsutils.EDNSSnapshot
type ECSInfo = dnsutils.ECSSnapshot

func (opts *EntryHandlerOpts) Init() error {
	if opts.Logger == nil {
		opts.Logger = nopLogger
	}
	if opts.Entry == nil {
		return errors.New("nil entry")
	}
	utils.SetDefaultNum(&opts.QueryTimeout, defaultQueryTimeout)
	return nil
}

type EntryHandler struct {
	opts EntryHandlerOpts
}

func NewEntryHandler(opts EntryHandlerOpts) (*EntryHandler, error) {
	if err := opts.Init(); err != nil {
		return nil, err
	}
	return &EntryHandler{opts: opts}, nil
}

// ServeDNS implements Handler.
// If entry returns an error, a SERVFAIL response will be returned.
// If entry returns without a response, a SERVFAIL response will be returned.
func (h *EntryHandler) ServeDNS(ctx context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	started := time.Now()
	result := Result{Rcode: -1}
	if h.opts.CaptureQueryDetails {
		result.AnswerIPs = []string{}
		result.EDNS = snapshotEDNS(req)
		result.EDNSTraceVersion = dnsutils.EDNSTraceVersion
		result.UpstreamStageStatus = query_context.UpstreamStageUnavailable
	}
	if meta != nil {
		result.Principal = meta.GetPrincipal()
		result.Protocol = meta.GetProtocol()
		result.ClientAddr = meta.GetClientAddr()
	}
	if h.opts.Observe != nil {
		defer func() {
			result.Duration = time.Since(started)
			h.opts.Observe(result)
		}()
	}
	// apply timeout to ctx
	ddl := time.Now().Add(h.opts.QueryTimeout)
	ctxDdl, ok := ctx.Deadline()
	if !(ok && ctxDdl.Before(ddl)) {
		newCtx, cancel := context.WithDeadline(ctx, ddl)
		defer cancel()
		ctx = newCtx
	}
	// return FORMERR response
	if len(req.Question) == 0 {
		h.opts.Logger.Warn("zero question")
		result.Rcode = dns.RcodeFormatError
		response := h.responseFormErr(req)
		result.UpstreamStageStatus = query_context.UpstreamStageNotLinked
		h.captureFinalResponse(&result, response)
		return response, nil
	}
	result.QuestionName = req.Question[0].Name
	result.QuestionType = req.Question[0].Qtype
	for _, question := range req.Question {
		_, ok := dns.IsDomainName(question.Name)
		if !ok {
			h.opts.Logger.Warn(fmt.Sprintf("invalid question name: %s", question.Name))
			result.Rcode = dns.RcodeFormatError
			response := h.responseFormErr(req)
			result.UpstreamStageStatus = query_context.UpstreamStageNotLinked
			h.captureFinalResponse(&result, response)
			return response, nil
		}
	}
	if h.opts.Admit != nil {
		if err := h.opts.Admit(ctx, result.Principal); err != nil {
			result.Rejected = true
			result.AccessKind, _ = query_access.KindOf(err)
			return nil, err
		}
	}
	result.Admitted = true
	// cache original id
	id := req.Id
	qCtx := query_context.NewContext(req, meta)
	qCtx.SetCaptureQueryDetails(h.opts.CaptureQueryDetails)
	err := h.opts.Entry.Exec(ctx, qCtx, nil)
	respMsg := qCtx.R()
	if err != nil {
		result.ExecError = true
		h.opts.Logger.Warn("query execution returned an err", zap.Error(err), qCtx.InfoField())
	} else {
		h.opts.Logger.Debug("entry returned", qCtx.InfoField())
	}
	if err == nil && respMsg == nil {
		h.opts.Logger.Error("query execution returned a nil response", qCtx.InfoField())
	}

	if respMsg == nil || err != nil {
		if h.opts.CaptureQueryDetails {
			trace := qCtx.ResponseTrace()
			if trace.UpstreamStageStatus == "" {
				result.UpstreamStageStatus = query_context.UpstreamStageNotLinked
			} else {
				result.UpstreamStageStatus = trace.UpstreamStageStatus
			}
			if result.UpstreamStageStatus == query_context.UpstreamStageSelected || trace.UpstreamID != "" {
				result.UpstreamStageStatus = query_context.UpstreamStageDiscarded
			}
		}
		respMsg = new(dns.Msg)
		respMsg.SetReply(req)
		respMsg.Rcode = dns.RcodeServerFailure
		result.ResponseSource = query_context.ResponseSourceServfail
	}

	if h.opts.RecursionAvailable {
		respMsg.RecursionAvailable = true
	}
	respMsg.Id = id
	result.Rcode = respMsg.Rcode
	if h.opts.CaptureQueryDetails {
		result.AnswerIPs = answerIPs(respMsg)
		h.captureFinalResponse(&result, respMsg)
	}
	if err == nil && result.ResponseSource != query_context.ResponseSourceServfail {
		result.CacheHit = qCtx.CacheHit()
		trace := qCtx.ResponseTrace()
		result.ResponseSource = trace.Source
		result.ResponseSourceID = trace.SourceID
		result.UpstreamID = trace.UpstreamID
		if h.opts.CaptureQueryDetails {
			result.UpstreamStageStatus = trace.UpstreamStageStatus
			result.UpstreamRequestEDNS = dnsutils.CloneEDNSSnapshot(trace.UpstreamRequestEDNS)
			result.UpstreamResponseEDNS = dnsutils.CloneEDNSSnapshot(trace.UpstreamResponseEDNS)
			if result.UpstreamStageStatus == "" {
				result.UpstreamStageStatus = query_context.UpstreamStageNotLinked
			}
		}
		if result.ResponseSource == "" {
			result.ResponseSource = query_context.ResponseSourceSequence
		}
	}
	return respMsg, nil
}

func (h *EntryHandler) captureFinalResponse(result *Result, response *dns.Msg) {
	if !h.opts.CaptureQueryDetails || response == nil {
		return
	}
	snapshot := dnsutils.SnapshotEDNS(response)
	result.ResponseEDNS = &snapshot
}

func answerIPs(msg *dns.Msg) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, answer := range msg.Answer {
		var addr netip.Addr
		var ok bool
		switch rr := answer.(type) {
		case *dns.A:
			if ip := rr.A.To4(); ip != nil {
				addr, ok = netip.AddrFromSlice(ip)
			}
		case *dns.AAAA:
			if ip := rr.AAAA.To16(); ip != nil {
				addr, ok = netip.AddrFromSlice(ip)
			}
		}
		if !ok {
			continue
		}
		value := addr.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func snapshotEDNS(msg *dns.Msg) EDNSInfo {
	return dnsutils.SnapshotEDNS(msg)
}

func (h *EntryHandler) responseFormErr(req *dns.Msg) *dns.Msg {
	res := new(dns.Msg)
	res.SetReply(req)
	res.Rcode = dns.RcodeFormatError
	if h.opts.RecursionAvailable {
		res.RecursionAvailable = true
	}
	return res
}

type DummyServerHandler struct {
	T       *testing.T
	WantMsg *dns.Msg
	WantErr error
}

func (d *DummyServerHandler) ServeDNS(_ context.Context, req *dns.Msg, meta *query_context.RequestMeta) (*dns.Msg, error) {
	if d.WantErr != nil {
		return nil, d.WantErr
	}

	var resp *dns.Msg
	if d.WantMsg != nil {
		resp = d.WantMsg.Copy()
		resp.Id = req.Id
	} else {
		resp = new(dns.Msg)
		resp.SetReply(req)
	}
	return resp, nil
}
