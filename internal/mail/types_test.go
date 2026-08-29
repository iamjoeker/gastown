package mail

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAddressToIdentity(t *testing.T) {
	tests := []struct {
		address  string
		expected string
	}{
		// Town-level agents keep trailing slash
		{"mayor", "mayor/"},
		{"mayor/", "mayor/"},
		{"deacon", "deacon/"},
		{"deacon/", "deacon/"},

		// Rig-scoped town-level roles resolve to canonical form (gt-te23)
		{"gastown/mayor", "mayor/"},
		{"gastown/deacon", "deacon/"},
		{"laser/mayor", "mayor/"},
		{"laser/deacon", "deacon/"},

		// Rig-level agents: crew/ and polecats/ normalized to canonical form
		{"gastown/polecats/Toast", "gastown/Toast"},
		{"gastown/crew/max", "gastown/max"},
		{"gastown/Toast", "gastown/Toast"}, // Already canonical
		{"gastown/max", "gastown/max"},     // Already canonical
		{"gastown/refinery", "gastown/refinery"},
		{"gastown/witness", "gastown/witness"},

		// Rig broadcast (trailing slash removed)
		{"gastown/", "gastown"},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := AddressToIdentity(tt.address)
			if got != tt.expected {
				t.Errorf("AddressToIdentity(%q) = %q, want %q", tt.address, got, tt.expected)
			}
		})
	}
}

func TestIdentityToAddress(t *testing.T) {
	tests := []struct {
		identity string
		expected string
	}{
		// Town-level agents
		{"mayor", "mayor/"},
		{"mayor/", "mayor/"},
		{"deacon", "deacon/"},
		{"deacon/", "deacon/"},

		// Rig-level agents: crew/ and polecats/ normalized
		{"gastown/polecats/Toast", "gastown/Toast"},
		{"gastown/crew/max", "gastown/max"},
		{"gastown/Toast", "gastown/Toast"}, // Already canonical
		{"gastown/refinery", "gastown/refinery"},
		{"gastown/witness", "gastown/witness"},

		// Rig name only (no transformation)
		{"gastown", "gastown"},
	}

	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			got := identityToAddress(tt.identity)
			if got != tt.expected {
				t.Errorf("identityToAddress(%q) = %q, want %q", tt.identity, got, tt.expected)
			}
		})
	}
}

func TestPriorityToBeads(t *testing.T) {
	tests := []struct {
		priority Priority
		expected int
	}{
		{PriorityUrgent, 0},
		{PriorityHigh, 1},
		{PriorityNormal, 2},
		{PriorityLow, 3},
		{Priority("unknown"), 2}, // Default to normal
	}

	for _, tt := range tests {
		t.Run(string(tt.priority), func(t *testing.T) {
			got := PriorityToBeads(tt.priority)
			if got != tt.expected {
				t.Errorf("PriorityToBeads(%q) = %d, want %d", tt.priority, got, tt.expected)
			}
		})
	}
}

func TestPriorityFromInt(t *testing.T) {
	tests := []struct {
		p        int
		expected Priority
	}{
		{0, PriorityUrgent},
		{1, PriorityHigh},
		{2, PriorityNormal},
		{3, PriorityLow},
		{4, PriorityLow},     // Out of range maps to low
		{-1, PriorityNormal}, // Negative maps to normal
	}

	for _, tt := range tests {
		got := PriorityFromInt(tt.p)
		if got != tt.expected {
			t.Errorf("PriorityFromInt(%d) = %q, want %q", tt.p, got, tt.expected)
		}
	}
}

func TestParsePriority(t *testing.T) {
	tests := []struct {
		s        string
		expected Priority
	}{
		{"urgent", PriorityUrgent},
		{"high", PriorityHigh},
		{"normal", PriorityNormal},
		{"low", PriorityLow},
		{"unknown", PriorityNormal}, // Default
		{"", PriorityNormal},        // Empty
		{"URGENT", PriorityNormal},  // Case-sensitive, defaults to normal
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := ParsePriority(tt.s)
			if got != tt.expected {
				t.Errorf("ParsePriority(%q) = %q, want %q", tt.s, got, tt.expected)
			}
		})
	}
}

func TestParseMessageType(t *testing.T) {
	tests := []struct {
		s        string
		expected MessageType
	}{
		{"task", TypeTask},
		{"escalation", TypeEscalation},
		{"scavenge", TypeScavenge},
		{"notification", TypeNotification},
		{"reply", TypeReply},
		{"unknown", TypeNotification}, // Default
		{"", TypeNotification},        // Empty
		{"TASK", TypeNotification},    // Case-sensitive, defaults to notification
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := ParseMessageType(tt.s)
			if got != tt.expected {
				t.Errorf("ParseMessageType(%q) = %q, want %q", tt.s, got, tt.expected)
			}
		})
	}
}

func TestNewMessage(t *testing.T) {
	msg := NewMessage("mayor/", "gastown/Toast", "Test Subject", "Test Body")

	if msg.From != "mayor/" {
		t.Errorf("From = %q, want 'mayor/'", msg.From)
	}
	if msg.To != "gastown/Toast" {
		t.Errorf("To = %q, want 'gastown/Toast'", msg.To)
	}
	if msg.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want 'Test Subject'", msg.Subject)
	}
	if msg.Body != "Test Body" {
		t.Errorf("Body = %q, want 'Test Body'", msg.Body)
	}
	if msg.ID == "" {
		t.Error("ID should be generated")
	}
	if msg.ThreadID == "" {
		t.Error("ThreadID should be generated")
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
	if msg.Priority != PriorityNormal {
		t.Errorf("Priority = %q, want PriorityNormal", msg.Priority)
	}
	if msg.Type != TypeNotification {
		t.Errorf("Type = %q, want TypeNotification", msg.Type)
	}
}

func TestNewReplyMessage(t *testing.T) {
	original := &Message{
		ID:       "orig-001",
		ThreadID: "thread-001",
		From:     "gastown/Toast",
		To:       "mayor/",
		Subject:  "Original Subject",
	}

	reply := NewReplyMessage("mayor/", "gastown/Toast", "Re: Original Subject", "Reply body", original)

	if reply.ThreadID != "thread-001" {
		t.Errorf("ThreadID = %q, want 'thread-001'", reply.ThreadID)
	}
	if reply.ReplyTo != "orig-001" {
		t.Errorf("ReplyTo = %q, want 'orig-001'", reply.ReplyTo)
	}
	if reply.From != "mayor/" {
		t.Errorf("From = %q, want 'mayor/'", reply.From)
	}
	if reply.To != "gastown/Toast" {
		t.Errorf("To = %q, want 'gastown/Toast'", reply.To)
	}
	if reply.Subject != "Re: Original Subject" {
		t.Errorf("Subject = %q, want 'Re: Original Subject'", reply.Subject)
	}
}

func TestBeadsMessageToMessage(t *testing.T) {
	now := time.Now()
	bm := BeadsMessage{
		ID:          "hq-test",
		Title:       "Test Subject",
		Description: "Test Body",
		Status:      "open",
		Assignee:    "gastown/Toast",
		Labels:      []string{"from:mayor/", "thread:t-001"},
		CreatedAt:   now,
		Priority:    1,
	}

	msg := bm.ToMessage()

	if msg.ID != "hq-test" {
		t.Errorf("ID = %q, want 'hq-test'", msg.ID)
	}
	if msg.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want 'Test Subject'", msg.Subject)
	}
	if msg.Body != "Test Body" {
		t.Errorf("Body = %q, want 'Test Body'", msg.Body)
	}
	if msg.From != "mayor/" {
		t.Errorf("From = %q, want 'mayor/'", msg.From)
	}
	if msg.ThreadID != "t-001" {
		t.Errorf("ThreadID = %q, want 't-001'", msg.ThreadID)
	}
	if msg.To != "gastown/Toast" {
		t.Errorf("To = %q, want 'gastown/Toast'", msg.To)
	}
	if msg.Priority != PriorityHigh {
		t.Errorf("Priority = %q, want PriorityHigh", msg.Priority)
	}
}

func TestBeadsMessageToMessageWithReplyTo(t *testing.T) {
	bm := BeadsMessage{
		ID:          "hq-reply",
		Title:       "Reply Subject",
		Description: "Reply Body",
		Status:      "open",
		Assignee:    "gastown/Toast",
		Labels:      []string{"from:mayor/", "thread:t-002", "reply-to:orig-001", "msg-type:reply"},
		CreatedAt:   time.Now(),
		Priority:    2,
	}

	msg := bm.ToMessage()

	if msg.ReplyTo != "orig-001" {
		t.Errorf("ReplyTo = %q, want 'orig-001'", msg.ReplyTo)
	}
	if msg.Type != TypeReply {
		t.Errorf("Type = %q, want TypeReply", msg.Type)
	}
}

func TestBeadsMessageToMessageWithEscalationTypeAndLabels(t *testing.T) {
	bm := BeadsMessage{
		ID:          "hq-esc",
		Title:       "Escalation subject",
		Description: "Escalation body",
		Status:      "open",
		Assignee:    "mayor",
		Labels: []string{
			"from:deacon/",
			"thread:t-esc",
			"msg-type:escalation",
			"gt:escalation",
			"severity:critical",
			"escalation:hq-abc123",
		},
		CreatedAt: time.Now(),
		Priority:  0,
	}

	msg := bm.ToMessage()

	if msg.Type != TypeEscalation {
		t.Errorf("Type = %q, want TypeEscalation", msg.Type)
	}
	if !bm.HasLabel("gt:escalation") {
		t.Error("expected gt:escalation label to be preserved")
	}
	if !bm.HasLabel("severity:critical") {
		t.Error("expected severity:critical label to be preserved")
	}
	if !bm.HasLabel("escalation:hq-abc123") {
		t.Error("expected escalation linkage label to be preserved")
	}
}

func TestBeadsMessageToMessagePriorities(t *testing.T) {
	tests := []struct {
		priority int
		expected Priority
	}{
		{0, PriorityUrgent},
		{1, PriorityHigh},
		{2, PriorityNormal},
		{3, PriorityLow},
		{4, PriorityNormal},  // Out of range defaults to normal
		{99, PriorityNormal}, // Out of range defaults to normal
	}

	for _, tt := range tests {
		bm := BeadsMessage{
			ID:       "hq-test",
			Priority: tt.priority,
		}
		msg := bm.ToMessage()
		if msg.Priority != tt.expected {
			t.Errorf("Priority %d -> %q, want %q", tt.priority, msg.Priority, tt.expected)
		}
	}
}

func TestBeadsMessageToMessageTypes(t *testing.T) {
	tests := []struct {
		msgType  string
		expected MessageType
	}{
		{"task", TypeTask},
		{"escalation", TypeEscalation},
		{"scavenge", TypeScavenge},
		{"reply", TypeReply},
		{"notification", TypeNotification},
		{"", TypeNotification}, // Default
	}

	for _, tt := range tests {
		bm := BeadsMessage{
			ID:     "hq-test",
			Labels: []string{"msg-type:" + tt.msgType},
		}
		msg := bm.ToMessage()
		if msg.Type != tt.expected {
			t.Errorf("msg-type:%s -> %q, want %q", tt.msgType, msg.Type, tt.expected)
		}
	}
}

func TestBeadsMessageToMessageEmptyLabels(t *testing.T) {
	bm := BeadsMessage{
		ID:          "hq-empty",
		Title:       "Empty Labels",
		Description: "Test with empty labels",
		Assignee:    "gastown/Toast",
		Labels:      []string{}, // No labels
		Priority:    2,
	}

	msg := bm.ToMessage()

	if msg.From != "" {
		t.Errorf("From should be empty, got %q", msg.From)
	}
	if msg.ThreadID != "" {
		t.Errorf("ThreadID should be empty, got %q", msg.ThreadID)
	}
}

func TestNewQueueMessage(t *testing.T) {
	msg := NewQueueMessage("mayor/", "work-requests", "New Task", "Please process this")

	if msg.From != "mayor/" {
		t.Errorf("From = %q, want 'mayor/'", msg.From)
	}
	if msg.Queue != "work-requests" {
		t.Errorf("Queue = %q, want 'work-requests'", msg.Queue)
	}
	if msg.To != "" {
		t.Errorf("To should be empty for queue messages, got %q", msg.To)
	}
	if msg.Channel != "" {
		t.Errorf("Channel should be empty for queue messages, got %q", msg.Channel)
	}
	if msg.Type != TypeTask {
		t.Errorf("Type = %q, want TypeTask", msg.Type)
	}
	if msg.ID == "" {
		t.Error("ID should be generated")
	}
	if msg.ThreadID == "" {
		t.Error("ThreadID should be generated")
	}
}

func TestNewChannelMessage(t *testing.T) {
	msg := NewChannelMessage("deacon/", "alerts", "System Alert", "System is healthy")

	if msg.From != "deacon/" {
		t.Errorf("From = %q, want 'deacon/'", msg.From)
	}
	if msg.Channel != "alerts" {
		t.Errorf("Channel = %q, want 'alerts'", msg.Channel)
	}
	if msg.To != "" {
		t.Errorf("To should be empty for channel messages, got %q", msg.To)
	}
	if msg.Queue != "" {
		t.Errorf("Queue should be empty for channel messages, got %q", msg.Queue)
	}
	if msg.Type != TypeNotification {
		t.Errorf("Type = %q, want TypeNotification", msg.Type)
	}
}

func TestMessageIsQueueMessage(t *testing.T) {
	directMsg := NewMessage("mayor/", "gastown/Toast", "Test", "Body")
	queueMsg := NewQueueMessage("mayor/", "work-requests", "Task", "Body")
	channelMsg := NewChannelMessage("deacon/", "alerts", "Alert", "Body")

	if directMsg.IsQueueMessage() {
		t.Error("Direct message should not be a queue message")
	}
	if !queueMsg.IsQueueMessage() {
		t.Error("Queue message should be a queue message")
	}
	if channelMsg.IsQueueMessage() {
		t.Error("Channel message should not be a queue message")
	}
}

func TestMessageIsChannelMessage(t *testing.T) {
	directMsg := NewMessage("mayor/", "gastown/Toast", "Test", "Body")
	queueMsg := NewQueueMessage("mayor/", "work-requests", "Task", "Body")
	channelMsg := NewChannelMessage("deacon/", "alerts", "Alert", "Body")

	if directMsg.IsChannelMessage() {
		t.Error("Direct message should not be a channel message")
	}
	if queueMsg.IsChannelMessage() {
		t.Error("Queue message should not be a channel message")
	}
	if !channelMsg.IsChannelMessage() {
		t.Error("Channel message should be a channel message")
	}
}

func TestMessageIsDirectMessage(t *testing.T) {
	directMsg := NewMessage("mayor/", "gastown/Toast", "Test", "Body")
	queueMsg := NewQueueMessage("mayor/", "work-requests", "Task", "Body")
	channelMsg := NewChannelMessage("deacon/", "alerts", "Alert", "Body")

	if !directMsg.IsDirectMessage() {
		t.Error("Direct message should be a direct message")
	}
	if queueMsg.IsDirectMessage() {
		t.Error("Queue message should not be a direct message")
	}
	if channelMsg.IsDirectMessage() {
		t.Error("Channel message should not be a direct message")
	}
}

func TestMessageValidate(t *testing.T) {
	tests := []struct {
		name    string
		msg     *Message
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid direct message",
			msg:     NewMessage("mayor/", "gastown/Toast", "Test", "Body"),
			wantErr: false,
		},
		{
			name:    "valid queue message",
			msg:     NewQueueMessage("mayor/", "work-requests", "Task", "Body"),
			wantErr: false,
		},
		{
			name:    "valid channel message",
			msg:     NewChannelMessage("deacon/", "alerts", "Alert", "Body"),
			wantErr: false,
		},
		{
			name: "missing ID",
			msg: &Message{
				From:    "mayor/",
				To:      "gastown/Toast",
				Subject: "Test",
			},
			wantErr: true,
			errMsg:  "must have an ID",
		},
		{
			name: "missing From",
			msg: &Message{
				ID:      "msg-001",
				To:      "gastown/Toast",
				Subject: "Test",
			},
			wantErr: true,
			errMsg:  "must have a From address",
		},
		{
			name: "missing Subject",
			msg: &Message{
				ID:   "msg-001",
				From: "mayor/",
				To:   "gastown/Toast",
			},
			wantErr: true,
			errMsg:  "must have a Subject",
		},
		{
			name: "no routing target",
			msg: &Message{
				ID:      "msg-001",
				From:    "mayor/",
				Subject: "Test",
			},
			wantErr: true,
			errMsg:  "must have exactly one of",
		},
		{
			name: "both to and queue",
			msg: &Message{
				ID:      "msg-001",
				From:    "mayor/",
				To:      "gastown/Toast",
				Queue:   "work-requests",
				Subject: "Test",
			},
			wantErr: true,
			errMsg:  "mutually exclusive",
		},
		{
			name: "both to and channel",
			msg: &Message{
				ID:      "msg-001",
				From:    "mayor/",
				To:      "gastown/Toast",
				Channel: "alerts",
				Subject: "Test",
			},
			wantErr: true,
			errMsg:  "mutually exclusive",
		},
		{
			name: "both queue and channel",
			msg: &Message{
				ID:      "msg-001",
				From:    "mayor/",
				Queue:   "work-requests",
				Channel: "alerts",
				Subject: "Test",
			},
			wantErr: true,
			errMsg:  "mutually exclusive",
		},
		{
			name: "claimed_by on non-queue message",
			msg: &Message{
				ID:        "msg-001",
				From:      "mayor/",
				To:        "gastown/Toast",
				Subject:   "Test",
				ClaimedBy: "gastown/nux",
			},
			wantErr: true,
			errMsg:  "claimed_by is only valid for queue messages",
		},
		{
			name: "claimed_by on queue message is valid",
			msg: &Message{
				ID:        "msg-001",
				From:      "mayor/",
				Queue:     "work-requests",
				Subject:   "Test",
				ClaimedBy: "gastown/nux",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				} else if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestBeadsMessageParseQueueChannelLabels(t *testing.T) {
	claimedTime := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	claimedAtStr := claimedTime.Format(time.RFC3339)

	bm := BeadsMessage{
		ID:          "hq-queue",
		Title:       "Queue Message",
		Description: "Test queue message",
		Status:      "open",
		Labels: []string{
			"from:mayor/",
			"queue:work-requests",
			"claimed-by:gastown/nux",
			"claimed-at:" + claimedAtStr,
		},
		Priority: 2,
	}

	msg := bm.ToMessage()

	if msg.Queue != "work-requests" {
		t.Errorf("Queue = %q, want 'work-requests'", msg.Queue)
	}
	if msg.ClaimedBy != "gastown/nux" {
		t.Errorf("ClaimedBy = %q, want 'gastown/nux'", msg.ClaimedBy)
	}
	if msg.ClaimedAt == nil {
		t.Error("ClaimedAt should not be nil")
	} else if !msg.ClaimedAt.Equal(claimedTime) {
		t.Errorf("ClaimedAt = %v, want %v", msg.ClaimedAt, claimedTime)
	}
}

func TestBeadsMessageParseChannelLabel(t *testing.T) {
	bm := BeadsMessage{
		ID:          "hq-channel",
		Title:       "Channel Message",
		Description: "Test channel message",
		Status:      "open",
		Labels:      []string{"from:deacon/", "channel:alerts"},
		Priority:    2,
	}

	msg := bm.ToMessage()

	if msg.Channel != "alerts" {
		t.Errorf("Channel = %q, want 'alerts'", msg.Channel)
	}
	if msg.Queue != "" {
		t.Errorf("Queue should be empty, got %q", msg.Queue)
	}
}

func TestBeadsMessageIsQueueMessage(t *testing.T) {
	queueMsg := BeadsMessage{
		ID:     "hq-queue",
		Labels: []string{"queue:work-requests"},
	}
	directMsg := BeadsMessage{
		ID:       "hq-direct",
		Assignee: "gastown/Toast",
	}
	channelMsg := BeadsMessage{
		ID:     "hq-channel",
		Labels: []string{"channel:alerts"},
	}

	if !queueMsg.IsQueueMessage() {
		t.Error("Queue message should be identified as queue message")
	}
	if directMsg.IsQueueMessage() {
		t.Error("Direct message should not be identified as queue message")
	}
	if channelMsg.IsQueueMessage() {
		t.Error("Channel message should not be identified as queue message")
	}
}

func TestBeadsMessageIsChannelMessage(t *testing.T) {
	queueMsg := BeadsMessage{
		ID:     "hq-queue",
		Labels: []string{"queue:work-requests"},
	}
	directMsg := BeadsMessage{
		ID:       "hq-direct",
		Assignee: "gastown/Toast",
	}
	channelMsg := BeadsMessage{
		ID:     "hq-channel",
		Labels: []string{"channel:alerts"},
	}

	if queueMsg.IsChannelMessage() {
		t.Error("Queue message should not be identified as channel message")
	}
	if directMsg.IsChannelMessage() {
		t.Error("Direct message should not be identified as channel message")
	}
	if !channelMsg.IsChannelMessage() {
		t.Error("Channel message should be identified as channel message")
	}
}

func TestBeadsMessageIsDirectMessage(t *testing.T) {
	queueMsg := BeadsMessage{
		ID:     "hq-queue",
		Labels: []string{"queue:work-requests"},
	}
	directMsg := BeadsMessage{
		ID:       "hq-direct",
		Assignee: "gastown/Toast",
	}
	channelMsg := BeadsMessage{
		ID:     "hq-channel",
		Labels: []string{"channel:alerts"},
	}

	if queueMsg.IsDirectMessage() {
		t.Error("Queue message should not be identified as direct message")
	}
	if !directMsg.IsDirectMessage() {
		t.Error("Direct message should be identified as direct message")
	}
	if channelMsg.IsDirectMessage() {
		t.Error("Channel message should not be identified as direct message")
	}
}

func TestMessageIsClaimed(t *testing.T) {
	unclaimed := NewQueueMessage("mayor/", "work-requests", "Task", "Body")
	if unclaimed.IsClaimed() {
		t.Error("Unclaimed message should not be claimed")
	}

	claimed := NewQueueMessage("mayor/", "work-requests", "Task", "Body")
	claimed.ClaimedBy = "gastown/nux"
	now := time.Now()
	claimed.ClaimedAt = &now

	if !claimed.IsClaimed() {
		t.Error("Claimed message should be claimed")
	}
}

func TestParseLabelsIdempotent(t *testing.T) {
	bm := BeadsMessage{
		ID:    "hq-test",
		Title: "Test",
		Labels: []string{
			"from:mayor/",
			"thread:t-001",
			"reply-to:orig-001",
			"msg-type:task",
			"cc:gastown/Toast",
			"cc:gastown/nux",
			"queue:work-requests",
			"channel:alerts",
			"claimed-by:gastown/nux",
			"delivery:pending",
			"delivery-acked-by:gastown/nux",
			"delivery-acked-at:2026-02-17T12:00:00Z",
			"delivery:acked",
		},
	}

	// Call ParseLabels multiple times
	bm.ParseLabels()
	bm.ParseLabels()
	bm.ParseLabels()

	// CC list should not accumulate duplicates
	if len(bm.cc) != 2 {
		t.Errorf("cc should have 2 entries after multiple ParseLabels calls, got %d: %v", len(bm.cc), bm.cc)
	}

	// Other fields should remain correct
	if bm.sender != "mayor/" {
		t.Errorf("sender = %q, want 'mayor/'", bm.sender)
	}
	if bm.threadID != "t-001" {
		t.Errorf("threadID = %q, want 't-001'", bm.threadID)
	}
	if bm.replyTo != "orig-001" {
		t.Errorf("replyTo = %q, want 'orig-001'", bm.replyTo)
	}
	if bm.msgType != "task" {
		t.Errorf("msgType = %q, want 'task'", bm.msgType)
	}
	if bm.queue != "work-requests" {
		t.Errorf("queue = %q, want 'work-requests'", bm.queue)
	}
	if bm.channel != "alerts" {
		t.Errorf("channel = %q, want 'alerts'", bm.channel)
	}
	if bm.claimedBy != "gastown/nux" {
		t.Errorf("claimedBy = %q, want 'gastown/nux'", bm.claimedBy)
	}
	if bm.deliveryState != DeliveryStateAcked {
		t.Errorf("deliveryState = %q, want %q", bm.deliveryState, DeliveryStateAcked)
	}
	if bm.deliveryAckedBy != "gastown/nux" {
		t.Errorf("deliveryAckedBy = %q, want %q", bm.deliveryAckedBy, "gastown/nux")
	}
}

func TestParseLabelsIdempotentViaPublicMethods(t *testing.T) {
	bm := BeadsMessage{
		ID:       "hq-test",
		Title:    "Test",
		Assignee: "gastown/Toast",
		Labels: []string{
			"from:mayor/",
			"cc:gastown/nux",
			"cc:gastown/slit",
		},
	}

	// Simulate the bug: calling IsDirectMessage then ToMessage
	// Both call ParseLabels internally
	_ = bm.IsDirectMessage()
	_ = bm.IsQueueMessage()
	_ = bm.IsChannelMessage()
	msg := bm.ToMessage()

	if len(msg.CC) != 2 {
		t.Errorf("CC should have 2 entries after multiple method calls, got %d: %v", len(msg.CC), msg.CC)
	}
}

func TestToMessage_DeliveryStatePendingOnPartialAck(t *testing.T) {
	bm := BeadsMessage{
		ID:       "hq-test",
		Title:    "Test",
		Assignee: "gastown/Toast",
		Labels: []string{
			"from:mayor/",
			"delivery:pending",
			"delivery-acked-by:gastown/Toast",
		},
	}

	msg := bm.ToMessage()
	if msg.DeliveryState != DeliveryStatePending {
		t.Fatalf("DeliveryState = %q, want %q", msg.DeliveryState, DeliveryStatePending)
	}
	if msg.DeliveryAckedBy != "" || msg.DeliveryAckedAt != nil {
		t.Fatalf("partial ack should not expose ack metadata, got by=%q at=%v", msg.DeliveryAckedBy, msg.DeliveryAckedAt)
	}
}

func TestToMessage_PinnedLabelRoundTrips(t *testing.T) {
	// gt-ho6r: msg.Pinned must survive the label write (messageIdentityLabels)
	// and read (ParseLabels -> ToMessage) round trip.
	labels := messageIdentityLabels(&Message{From: "mayor/", Type: TypeNotification, Pinned: true})
	found := false
	for _, l := range labels {
		if l == "gt:pinned" {
			found = true
		}
	}
	if !found {
		t.Fatalf("messageIdentityLabels(Pinned: true) did not include gt:pinned, got %v", labels)
	}

	bm := BeadsMessage{
		ID:       "hq-test",
		Title:    "Test",
		Assignee: "gastown/Toast",
		Labels:   labels,
	}
	msg := bm.ToMessage()
	if !msg.Pinned {
		t.Fatalf("Message.Pinned = false, want true after round trip through labels %v", labels)
	}

	// Unpinned messages must not carry the label or read back as pinned.
	unpinnedLabels := messageIdentityLabels(&Message{From: "mayor/", Type: TypeNotification})
	bm2 := BeadsMessage{ID: "hq-test2", Assignee: "gastown/Toast", Labels: unpinnedLabels}
	if msg2 := bm2.ToMessage(); msg2.Pinned {
		t.Fatalf("Message.Pinned = true for unpinned send, labels=%v", unpinnedLabels)
	}
}

func TestSuppressNotifyNotSerialized(t *testing.T) {
	msg := NewMessage("mayor/", "gastown/Toast", "Test", "Body")
	msg.SuppressNotify = true

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// SuppressNotify should not appear in JSON output (json:"-" tag)
	if containsString(string(data), "SuppressNotify") || containsString(string(data), "suppress") {
		t.Errorf("SuppressNotify should not be serialized, but found in JSON: %s", data)
	}

	// Roundtrip: unmarshal should leave SuppressNotify as false (zero value)
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.SuppressNotify {
		t.Error("SuppressNotify should be false after roundtrip (not deserialized)")
	}
}

func TestNewMessageValidatesForCrossRigAddresses(t *testing.T) {
	// Regression test: cross-rig addresses like "beads/crew/emma" must have
	// auto-generated ID and pass validation (gt-rud3p).
	crossRigAddresses := []string{
		"beads/crew/emma",
		"gastown/polecats/Toast",
		"otherrig/witness",
		"mayor/",
	}

	for _, addr := range crossRigAddresses {
		t.Run(addr, func(t *testing.T) {
			msg := NewMessage("gastown/dag", addr, "Test subject", "Test body")

			if msg.ID == "" {
				t.Error("NewMessage must generate a non-empty ID")
			}
			if msg.ThreadID == "" {
				t.Error("NewMessage must generate a non-empty ThreadID")
			}

			if err := msg.Validate(); err != nil {
				t.Errorf("NewMessage for %q should produce a valid message, got: %v", addr, err)
			}
		})
	}
}

func TestNewMessageFanOutCopiesGetUniqueIDs(t *testing.T) {
	// When fanning out to multiple recipients, copies with cleared IDs
	// should get unique IDs from sendToSingle (gt-rud3p).
	msg := NewMessage("gastown/dag", "beads/crew/emma", "Test", "Body")
	originalID := msg.ID

	if originalID == "" {
		t.Fatal("original message must have an ID")
	}

	// Simulate fan-out: create a copy and clear its ID
	msgCopy := *msg
	msgCopy.To = "otherrig/crew/bob"
	msgCopy.ID = ""

	if msgCopy.ID == originalID {
		t.Error("fan-out copy ID should be cleared, not match original")
	}

	// The cleared copy should fail validation (sendToSingle regenerates it)
	if err := msgCopy.Validate(); err == nil {
		t.Error("copy with empty ID should fail validation before sendToSingle regenerates it")
	}
}

// --- gt-do5c: msg-type must be able to discriminate ---------------------------
//
// The bug was that msg-type was uniformly "notification": 397 open beads carried
// it while "query" and "handoff" were both zero. The zeros had two different
// causes and both are regression-tested here.

// TestParseMessageTypeAcceptsQueryAndHandoff pins the first cause: "query" and
// "handoff" were not members of the enum, so ParseMessageType coerced them to
// TypeNotification. Their count of zero described this switch statement, not
// sender behaviour.
func TestParseMessageTypeAcceptsQueryAndHandoff(t *testing.T) {
	for _, want := range []MessageType{TypeQuery, TypeHandoff} {
		if got := ParseMessageType(string(want)); got != want {
			t.Errorf("ParseMessageType(%q) = %q, want %q (silently coerced — the gt-do5c bug)", want, got, want)
		}
	}
}

// TestParseMessageTypeRoundTripsEveryValidType is the self-validating control
// for the test above: a probe that only checked query and handoff could pass
// against an enum that had lost every other member.
func TestParseMessageTypeRoundTripsEveryValidType(t *testing.T) {
	for _, want := range ValidMessageTypes() {
		if got := ParseMessageType(string(want)); got != want {
			t.Errorf("ParseMessageType(%q) = %q, want round-trip", want, got)
		}
		if !want.IsValid() {
			t.Errorf("%q is listed by ValidMessageTypes but IsValid says otherwise", want)
		}
	}
}

// TestParseMessageTypeStaysLenientForStorage: reading a bead written by another
// build must not fail, so unrecognised values still degrade to notification on
// the READ path. Only caller input is rejected (see ValidateMessageType).
func TestParseMessageTypeStaysLenientForStorage(t *testing.T) {
	for _, in := range []string{"", "bogus", "NOTIFICATION", "future-type"} {
		if got := ParseMessageType(in); got != TypeNotification {
			t.Errorf("ParseMessageType(%q) = %q, want TypeNotification on the read path", in, got)
		}
	}
}

// TestValidateMessageTypeRejectsUnknown pins the mechanism that made the field
// uniform: `gt mail send --type query` reported success and stored
// msg-type:notification, so a sender trying to be honest was overruled silently.
func TestValidateMessageTypeRejectsUnknown(t *testing.T) {
	if _, err := ValidateMessageType("query"); err != nil {
		t.Fatalf("ValidateMessageType(query) errored: %v", err)
	}
	got, err := ValidateMessageType("")
	if err != nil || got != TypeNotification {
		t.Errorf("ValidateMessageType(\"\") = %q, %v; want notification with no error", got, err)
	}
	for _, bad := range []string{"bogus", "Query", "notifcation"} {
		got, err := ValidateMessageType(bad)
		if err == nil {
			t.Errorf("ValidateMessageType(%q) = %q with no error; want rejection, not silent coercion", bad, got)
		}
		if got != "" {
			t.Errorf("ValidateMessageType(%q) returned %q alongside its error; want the zero value", bad, got)
		}
	}
}

// TestValidateMessageTypeErrorNamesTheValidValues: an error that does not say
// what to type instead sends the caller back to guessing, which is how "query"
// came to be used in prose but never in the enum.
func TestValidateMessageTypeErrorNamesTheValidValues(t *testing.T) {
	_, err := ValidateMessageType("bogus")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range ValidMessageTypes() {
		if !strings.Contains(err.Error(), string(want)) {
			t.Errorf("error %q does not mention valid type %q", err, want)
		}
	}
}

// TestCloseOnReadSemantics is the predicate gt-qffl (close-on-read) and any
// future mail GC depend on. Getting a row of this table wrong either loses a
// question someone is blocked on, or leaves consumed mail in the work queue.
func TestCloseOnReadSemantics(t *testing.T) {
	tests := []struct {
		msgType     MessageType
		expectReply bool
		safeToClose bool
	}{
		{TypeNotification, false, true},    // nothing can be replied to
		{TypeReply, false, true},           // an answer owes nothing back
		{TypeHandoff, false, true},         // consumed by the successor reading it
		{TypeQuery, true, false},           // someone is blocked on the answer
		{TypeTask, true, false},            // reading is not doing
		{TypeScavenge, true, false},        // unclaimed work is still work
		{TypeEscalation, true, false},      // has its own ack surface; never auto-close
		{MessageType(""), true, false},     // a writer that forgot to stamp a type
		{MessageType("nope"), true, false}, // written by a build we do not know
	}
	for _, tt := range tests {
		if got := tt.msgType.ExpectsReply(); got != tt.expectReply {
			t.Errorf("%q.ExpectsReply() = %v, want %v", tt.msgType, got, tt.expectReply)
		}
		if got := tt.msgType.SafeToCloseOnRead(); got != tt.safeToClose {
			t.Errorf("%q.SafeToCloseOnRead() = %v, want %v", tt.msgType, got, tt.safeToClose)
		}
	}
}

// TestSafeToCloseOnReadFailsClosed states the direction of the predicate's bias
// on its own, so a future edit cannot flip it while the table above still reads
// as if it passes. Under-classifying is the dangerous direction: a real question
// mistaken for a notification gets closed and the sender waits forever.
func TestSafeToCloseOnReadFailsClosed(t *testing.T) {
	for _, unknown := range []MessageType{"", "notifcation", "QUERY", "request", "ping"} {
		if unknown.SafeToCloseOnRead() {
			t.Errorf("%q.SafeToCloseOnRead() = true; an unrecognised type must never auto-close", unknown)
		}
	}
	// Control: the predicate must still be able to return true, or the check
	// above passes against a function that is simply hardwired to false.
	if !TypeNotification.SafeToCloseOnRead() {
		t.Error("TypeNotification.SafeToCloseOnRead() = false; the predicate can never say yes")
	}
}

// TestBeadsMessageToMessageParsesNewTypes covers the second half of the drift:
// ToMessage carried its own copy of ParseMessageType's case list, so a type
// added to one would still read back as "notification" through the other.
func TestBeadsMessageToMessageParsesNewTypes(t *testing.T) {
	for _, want := range ValidMessageTypes() {
		bm := BeadsMessage{ID: "hq-test", Labels: []string{"msg-type:" + string(want)}}
		if got := bm.ToMessage().Type; got != want {
			t.Errorf("msg-type:%s read back as %q, want %q", want, got, want)
		}
	}
}
