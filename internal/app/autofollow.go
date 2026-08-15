package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/youtube"
)

// Auto-follow: when a watched stream ends and the same channel goes live
// again, re-resolve the channel and reconnect to the new broadcast.
//
// It is opt-in (auto_follow, default off) because every check is a
// quota-charged channels.list call. The watch is bounded twice over: checks
// run no more often than auto_follow_poll_seconds (floored at
// minAutoFollowInterval) and stop for good after auto_follow_max_checks, so
// the worst case is a known, small number of estimated units - about 30 with
// the defaults - rather than an open-ended drain on the budget that defines
// this client (docs/adr/0004).

// minAutoFollowInterval is the fastest cadence auto-follow will check at, no
// matter what the config asks for. A one-second typo would otherwise spend the
// whole check budget in half a minute.
const minAutoFollowInterval = 30 * time.Second

// autoFollowTickMsg asks for one re-resolution of an ended chat's channel.
type autoFollowTickMsg struct {
	chatKey string
}

// autoFollowResolvedMsg is the outcome of one check.
type autoFollowResolvedMsg struct {
	chatKey string
	target  youtube.ChatTarget
	err     error
}

// autoFollowIntervalFor clamps the configured cadence.
func autoFollowIntervalFor(seconds int) time.Duration {
	interval := time.Duration(seconds) * time.Second
	if interval < minAutoFollowInterval {
		return minAutoFollowInterval
	}
	return interval
}

// autoFollowMaxChecksFor applies the default check cap. Zero and negative both
// mean "default" rather than "unbounded": an unbounded watch is never what a
// quota-priced API should be handed by accident.
func autoFollowMaxChecksFor(checks int) int {
	if checks <= 0 {
		return 30
	}
	return checks
}

// maybeStartAutoFollow arms the watch for a chat whose live chat just closed.
//
// It needs three things: the feature on, a resolver to ask, and a channel to
// ask about. A chat opened by bare video ID may not know its channel until the
// metrics lookup answers; in that case there is nothing to follow and the
// ended state stands, exactly as it does with the feature off.
func (m *shellModel) maybeStartAutoFollow(chatID string) tea.Cmd {
	if !m.autoFollowEnabled || m.broadcastResolver == nil || m.chats == nil {
		return nil
	}
	state := m.chats.stateForChatID(chatID)
	if state == nil || state.autoFollowActive {
		return nil
	}
	if autoFollowProbeTarget(state.target).Key() == "" {
		return nil
	}
	state.autoFollowActive = true
	state.autoFollowChecks = 0
	state.status.Detail = "ended; auto-follow is watching for the next stream"
	return m.scheduleAutoFollowTick(state.key)
}

// scheduleAutoFollowTick arms the next check for one chat.
func (m *shellModel) scheduleAutoFollowTick(chatKey string) tea.Cmd {
	interval := m.autoFollowInterval
	if interval <= 0 {
		interval = minAutoFollowInterval
	}
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return autoFollowTickMsg{chatKey: chatKey}
	})
}

// handleAutoFollowTick spends one check: it re-resolves the ended chat's
// channel in a command and reports back as autoFollowResolvedMsg.
func (m shellModel) handleAutoFollowTick(msg autoFollowTickMsg) (tea.Model, tea.Cmd) {
	state := m.chats.ensureKey(msg.chatKey)
	if state == nil || !state.autoFollowActive || m.broadcastResolver == nil {
		return m, nil
	}
	resolver := m.broadcastResolver
	probe := autoFollowProbeTarget(state.target)
	if probe.Key() == "" {
		state.autoFollowActive = false
		return m, nil
	}
	chatKey := state.key
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resolved, err := resolver.ResolveTarget(ctx, probe)
		return autoFollowResolvedMsg{chatKey: chatKey, target: resolved, err: err}
	}
}

// handleAutoFollowResolved folds one check's outcome back into the chat.
//
// "Still offline" and a transient failure are treated alike - count the check,
// wait, try again - because distinguishing them buys nothing: either way the
// only move is another bounded check. A resolution that returns the same
// liveChatId the ended session had is also "still offline": chat outlives the
// broadcast, so the old chat can stay resolvable for a short window after it
// closed.
func (m shellModel) handleAutoFollowResolved(msg autoFollowResolvedMsg) (tea.Model, tea.Cmd) {
	state := m.chats.ensureKey(msg.chatKey)
	if state == nil || !state.autoFollowActive {
		return m, nil
	}

	sameChat := msg.err == nil &&
		strings.EqualFold(strings.TrimSpace(msg.target.LiveChatID), strings.TrimSpace(state.target.LiveChatID))
	if msg.err != nil || sameChat {
		state.autoFollowChecks++
		if state.autoFollowChecks >= m.autoFollowMaxChecks {
			state.autoFollowActive = false
			state.status.Detail = fmt.Sprintf("ended; auto-follow stopped after %d checks", state.autoFollowChecks)
			return m, nil
		}
		state.status.Detail = fmt.Sprintf("ended; auto-follow is watching for the next stream (check %d/%d)",
			state.autoFollowChecks, m.autoFollowMaxChecks)
		return m, m.scheduleAutoFollowTick(state.key)
	}

	// A new broadcast. Adopt its identifiers into the existing chat state so
	// history, drafts, and scroll position survive, then open a transport
	// session for the new live chat. The old session parked when its chat
	// ended; closing it by the routing key frees it before its replacement
	// starts, so two sessions never poll for one on-screen chat.
	state.autoFollowActive = false
	state.mergeTarget(msg.target)
	state.ended = false
	state.liveKnown = false
	state.status = youtube.ConnectionState{
		Status: youtube.ConnectionConnecting,
		ChatID: state.target.LiveChatID,
		Detail: "auto-follow found a new stream; connecting",
		At:     m.now(),
	}

	var cmds []tea.Cmd
	if joiner, ok := m.client.(ChatJoiner); ok {
		oldKey := state.key
		target := msg.target
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = joiner.CloseChat(oldKey)
			resolved, err := joiner.OpenChat(ctx, target)
			return targetResolvedMsg{chatKey: oldKey, target: resolved, err: err}
		})
	}
	cmds = append(cmds, m.refreshChatMetricsCommand())
	return m, batchNonNil(cmds...)
}

// autoFollowProbeTarget builds the channel-shaped target a check resolves. The
// channel ID is preferred - it is immutable and resolves for one published
// unit - with the handle as the fallback for a chat that never learned it.
func autoFollowProbeTarget(target youtube.ChatTarget) youtube.ChatTarget {
	if id := strings.TrimSpace(target.ChannelID); id != "" {
		return youtube.ChatTarget{Raw: id, Kind: youtube.TargetChannelID, ChannelID: id}
	}
	if handle := strings.TrimSpace(target.Handle); handle != "" {
		return youtube.ChatTarget{Raw: handle, Kind: youtube.TargetHandle, Handle: handle}
	}
	return youtube.ChatTarget{}
}
