package agentservice

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestChatContentPartsBuildsImageAndTextParts(t *testing.T) {
	attachments := []ChatAttachment{
		{Filename: "chart.png", ContentType: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte("png"))},
		{Filename: "notes.txt", ContentType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte("说明"))},
	}
	parts, err := ChatContentParts("请分析", attachments)
	if err != nil {
		t.Fatalf("ChatContentParts() error = %v", err)
	}
	if len(parts) != 3 || parts[0].Type != "text" || parts[1].Type != "image_url" || parts[2].Type != "text" {
		t.Fatalf("parts = %#v, want text/image/text", parts)
	}
	if parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,"+attachments[0].DataBase64 {
		t.Fatalf("image URL = %#v", parts[1].ImageURL)
	}
	if !strings.Contains(parts[2].Text, "notes.txt") || !strings.Contains(parts[2].Text, "说明") {
		t.Fatalf("text attachment part = %q", parts[2].Text)
	}
}

func TestValidateAttachmentsRejectsUnsafeAndOversizedInput(t *testing.T) {
	tests := []struct {
		name       string
		attachment ChatAttachment
		wantErr    error
	}{
		{name: "unsupported type", attachment: ChatAttachment{Filename: "run.exe", ContentType: "application/octet-stream", DataBase64: "YQ=="}, wantErr: ErrInvalidAttachment},
		{name: "invalid base64", attachment: ChatAttachment{Filename: "note.txt", ContentType: "text/plain", DataBase64: "not-base64"}, wantErr: ErrInvalidAttachment},
		{name: "oversized text", attachment: ChatAttachment{Filename: "note.txt", ContentType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", MaxTextAttachmentBytes+1)))}, wantErr: ErrAttachmentTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAttachments([]ChatAttachment{tt.attachment})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr.Error()) {
				t.Fatalf("ValidateAttachments() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
	if err := ValidateAttachments(make([]ChatAttachment, MaxChatAttachments+1)); err == nil {
		t.Fatal("ValidateAttachments() error = nil, want attachment count error")
	}
}

func TestNormalizeThinkingMode(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{input: "", want: "standard"},
		{input: " DEEP ", want: "deep"},
		{input: "fast", want: "fast"},
	} {
		got, err := NormalizeThinkingMode(test.input)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeThinkingMode(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	if _, err := NormalizeThinkingMode("turbo"); err != ErrInvalidThinkingMode {
		t.Fatalf("NormalizeThinkingMode() error = %v, want %v", err, ErrInvalidThinkingMode)
	}
}
