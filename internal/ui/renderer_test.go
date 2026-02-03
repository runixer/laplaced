package ui

import (
	"strings"
	"testing"
)

func TestNewRenderer(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	if r == nil {
		t.Fatal("NewRenderer() returned nil")
	}

	if r.layout == nil {
		t.Error("NewRenderer() layout is nil")
	}
}

func TestRenderer_Render_Success(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	var sb strings.Builder
	data := PageData{
		SelectedUserID: 123,
	}

	err = r.Render(&sb, "test.html", data, GetFuncMap())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	result := sb.String()
	if result == "" {
		t.Error("Render() produced empty output")
	}

	// Check that layout template was used (should contain HTML structure)
	if !strings.Contains(result, "<html") && !strings.Contains(result, "<div") {
		t.Error("Render() output should contain HTML")
	}
}

func TestRenderer_Render_WithoutFuncMap(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	var sb strings.Builder
	data := PageData{
		SelectedUserID: 123,
	}

	err = r.Render(&sb, "test.html", data, nil)
	if err != nil {
		t.Fatalf("Render() without funcMap error = %v", err)
	}

	result := sb.String()
	if result == "" {
		t.Error("Render() produced empty output")
	}
}

func TestRenderer_Render_InvalidTemplate(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	var sb strings.Builder
	data := PageData{
		SelectedUserID: 123,
	}

	err = r.Render(&sb, "nonexistent.html", data, GetFuncMap())
	if err == nil {
		t.Error("Render() with invalid template should return error")
	}

	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("Error message should mention parsing failure, got: %v", err)
	}
}
