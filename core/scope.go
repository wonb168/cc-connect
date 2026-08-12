package core

import (
	"path/filepath"
	"strings"
)

// SessionScope describes the chat/user isolation coordinates of a single
// conversation. It is derived from a session key (see ScopeFromSessionKey) and
// handed to agent sessions that opt into per-chat/per-user directory scoping
// via the ScopedSession capability interface.
//
// ChatID is the platform chat identifier (group or DM). UID is the in-domain
// user identity within that chat (for Feishu this is the union_id; see the
// "隔离与会话记录模型" section of CONTEXT.md). UID may be empty when the
// session is shared across a channel (shareSessionInChannel mode), in which
// case scoping degrades to chat level only.
type SessionScope struct {
	ChatID string
	UID    string
}

// ScopedSession is an optional capability interface. Agent sessions that want
// to lay out on-disk artifacts (e.g. input attachments) under a per-chat
// directory implement it; the engine calls SetScope right after StartSession.
// Sessions that don't implement it are unaffected — scoping is opt-in per the
// capability-check pattern (never a hardcoded agent-name check in core).
type ScopedSession interface {
	SetScope(SessionScope)
}

// ScopeFromSessionKey derives a SessionScope from a session key. It reuses
// ParseSessionKey, so it stays consistent with the three supported key
// formats:
//
//	"platform:chatID:userID"        → ChatID=chatID, UID=userID
//	"platform:chatID"               → ChatID=chatID, UID="" (channel-shared)
//	"platform:chatID:root:rootID"   → ChatID=chatID, UID="root:rootID"
//
// The platform prefix is stripped from the baseChat so ChatID is the bare
// chat identifier the platform reported.
func ScopeFromSessionKey(sessionKey string) SessionScope {
	platform, baseChat, userOrThread := ParseSessionKey(sessionKey)
	chatID := baseChat
	if prefix := platform + ":"; strings.HasPrefix(baseChat, prefix) {
		chatID = strings.TrimPrefix(baseChat, prefix)
	}
	return SessionScope{ChatID: chatID, UID: userOrThread}
}

// applySessionScope hands per-chat/per-user coordinates to an agent session
// that opts into directory scoping via ScopedSession. The capability check
// keeps core agnostic: sessions that don't implement ScopedSession are
// unaffected. Called by the engine right after StartSession at each start
// site (main dispatch and relay).
func applySessionScope(as AgentSession, sessionKey string) {
	if sc, ok := as.(ScopedSession); ok {
		sc.SetScope(ScopeFromSessionKey(sessionKey))
	}
}

// ScopedAttachmentsDir returns the on-disk directory where user-supplied input
// attachments for a given chat are stored:
//
//	<workDir>/Chat/<chatID>/Inputs
//
// chatID is sanitized to a single safe path segment. This is THE canonical
// computation — both the agent read path (claudeSession.attachmentsRoot) and
// the engine's silent-archive write path (Message.SaveOnly → archiveAttachments)
// call this function, guaranteeing a file archived without an agent turn lands
// in the exact directory a later @bot turn reads from.
func ScopedAttachmentsDir(workDir, chatID string) string {
	return filepath.Join(workDir, "Chat", sanitizePathSegment(chatID), "Inputs")
}

// sanitizePathSegment reduces an untrusted identifier (e.g. a platform chat ID)
// to a single safe path segment: path separators and traversal are stripped,
// and any leftover empty/dotted result falls back to a stable placeholder so
// callers never join "" or "..".
func sanitizePathSegment(s string) string {
	s = filepath.Base(strings.TrimSpace(s))
	if s == "" || s == "." || s == ".." || s == string(filepath.Separator) {
		return "_"
	}
	return s
}
