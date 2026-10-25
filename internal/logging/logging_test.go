// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package logging

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %s", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	defer func() {
		os.Stderr = origStderr
		r.Close()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %s", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %s", err)
	}
	return string(out)
}

func wireStdLogger(t *testing.T, w io.Writer) {
	t.Helper()

	origWriter := log.Writer()
	origFlags := log.Flags()
	origPrefix := log.Prefix()
	t.Cleanup(func() {
		log.SetFlags(origFlags)
		log.SetPrefix(origPrefix)
		log.SetOutput(origWriter)
	})

	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(w)
}

func stdWriterFor() (hclog.Logger, io.Writer) {
	logger := newHCLogger("")
	return logger, logger.StandardWriter(&hclog.StandardLoggerOptions{InferLevels: true})
}

func TestStdLoggerSilentWhenLoggingDisabled(t *testing.T) {
	t.Setenv("TF_LOG", "")

	captured := captureStderr(t, func() {
		_, stdWriter := stdWriterFor()
		wireStdLogger(t, stdWriter)
		if log.Writer() != io.Writer(stdWriter) {
			t.Fatal("test setup: standard logger is not wired to the fresh writer")
		}
		log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "random")
	})

	if captured != "" {
		t.Errorf("expected no log output when logging is disabled, got:\n%s", captured)
	}
}

func TestStdLoggerSilentWithExplicitOffLevel(t *testing.T) {
	t.Setenv("TF_LOG", "off")

	captured := captureStderr(t, func() {
		_, stdWriter := stdWriterFor()
		wireStdLogger(t, stdWriter)
		if log.Writer() != io.Writer(stdWriter) {
			t.Fatal("test setup: standard logger is not wired to the fresh writer")
		}
		log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "random")
	})

	if captured != "" {
		t.Errorf("expected no log output at TF_LOG=off, got:\n%s", captured)
	}
}

func TestStdLoggerEmitsWhenLoggingEnabled(t *testing.T) {
	t.Setenv("TF_LOG", "trace")

	captured := captureStderr(t, func() {
		_, stdWriter := stdWriterFor()
		wireStdLogger(t, stdWriter)
		log.Printf("[DEBUG] hello %s", "world")
	})

	if !strings.Contains(captured, "[DEBUG] hello world") {
		t.Errorf("expected the logged message in the output, got:\n%s", captured)
	}
}

func TestStdLoggerEmitsJSONWhenJSONFormatRequested(t *testing.T) {
	t.Setenv("TF_LOG", "JSON")

	captured := captureStderr(t, func() {
		_, stdWriter := stdWriterFor()
		wireStdLogger(t, stdWriter)
		log.Printf("[DEBUG] hello %s", "world")
	})

	line := strings.TrimSpace(captured)
	if !strings.HasPrefix(line, "{") {
		t.Fatalf("expected a JSON log line, got:\n%s", captured)
	}
	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("log line is not valid JSON: %s\nline:\n%s", err, captured)
	}
	if entry["@message"] != "hello world" {
		t.Errorf("expected @message to be %q, got %v", "hello world", entry["@message"])
	}
	if entry["@level"] != "debug" {
		t.Errorf("expected @level to be %q, got %v", "debug", entry["@level"])
	}
}

func TestProcessStdLoggerDeliversToSinkWhileDisabled(t *testing.T) {
	if os.Getenv("TF_LOG") != "" || os.Getenv("TF_LOG_CORE") != "" {
		t.Skip("requires TF_LOG and TF_LOG_CORE to be unset")
	}
	if log.Writer() != io.Discard {
		t.Fatalf("test precondition: standard logger output is %T, want io.Discard", log.Writer())
	}

	sinkPath := filepath.Join(t.TempDir(), "sink.log")
	f, err := os.OpenFile(sinkPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		t.Fatalf("open sink file: %s", err)
	}
	defer f.Close()

	intercept, ok := logger.(hclog.InterceptLogger)
	if !ok {
		t.Fatal("global logger is not an InterceptLogger")
	}
	adapter := hclog.NewSinkAdapter(&hclog.LoggerOptions{
		Level:  hclog.Trace,
		Output: f,
	})

	origWriter := log.Writer()
	RegisterSink(f)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		sinksRegistered.Store(false)
		intercept.DeregisterSink(adapter)
	})

	captured := captureStderr(t, func() {
		log.Printf("[DEBUG] hello %s", "world")
	})

	if strings.Contains(captured, "hello world") {
		t.Errorf("primary output unexpectedly received the message while Off:\n%s", captured)
	}

	data, err := os.ReadFile(sinkPath)
	if err != nil {
		t.Fatalf("read sink file: %s", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Errorf("sink did not receive the standard-logger message while logging is disabled; sink file:\n%s", data)
	}
}

func TestStdLoggerMessagesReachSinksWhileEnabled(t *testing.T) {
	t.Setenv("TF_LOG", "trace")

	sinkPath := filepath.Join(t.TempDir(), "sink.log")
	f, err := os.OpenFile(sinkPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		t.Fatalf("open sink file: %s", err)
	}
	defer f.Close()

	captured := captureStderr(t, func() {
		logger := newHCLogger("")
		intercept, ok := logger.(hclog.InterceptLogger)
		if !ok {
			t.Fatal("fresh logger is not an InterceptLogger")
		}
		adapter := hclog.NewSinkAdapter(&hclog.LoggerOptions{
			Level:  hclog.Trace,
			Output: f,
		})
		intercept.RegisterSink(adapter)
		t.Cleanup(func() { intercept.DeregisterSink(adapter) })

		stdWriter := logger.StandardWriter(&hclog.StandardLoggerOptions{InferLevels: true})
		wireStdLogger(t, stdWriter)
		if log.Writer() != io.Writer(stdWriter) {
			t.Fatal("test setup: standard logger is not wired to the fresh writer")
		}

		log.Printf("[DEBUG] hello %s", "world") // through the standard logger
		logger.Info("direct call")              // through the hclog API
	})

	data, err := os.ReadFile(sinkPath)
	if err != nil {
		t.Fatalf("read sink file: %s", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Errorf("sink stopped capturing standard-logger messages while logging is enabled:\n%s", data)
	}
	if !strings.Contains(string(data), "direct call") {
		t.Errorf("sink stopped capturing direct hclog calls:\n%s", data)
	}
	if !strings.Contains(captured, "hello world") {
		t.Errorf("primary output lost the standard-logger message while logging is enabled:\n%s", captured)
	}
}

func TestTFLogPathFileCreatedAndWrittenWhenEnabled(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "terraform.log")
	t.Setenv("TF_LOG", "trace")
	t.Setenv("TF_LOG_PATH", logPath)

	logger, stdWriter := stdWriterFor()
	wireStdLogger(t, stdWriter)
	logger.Info("direct call")
	log.Printf("[DEBUG] hello %s", "world")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %s", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Errorf("expected the logged message in TF_LOG_PATH file, got:\n%s", data)
	}
}

func TestTFLogPathFileStaysEmptyWhenDisabled(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "terraform.log")
	t.Setenv("TF_LOG", "")
	t.Setenv("TF_LOG_PATH", logPath)

	_, stdWriter := stdWriterFor()
	wireStdLogger(t, stdWriter)
	if log.Writer() != io.Writer(stdWriter) {
		t.Fatal("test setup: standard logger is not wired to the fresh writer")
	}
	log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "random")

	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("TF_LOG_PATH file was not created: %s", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %s", err)
	}
	if len(data) != 0 {
		t.Errorf("expected TF_LOG_PATH file to stay empty when logging is disabled, got:\n%s", data)
	}
}

func TestProviderLoggerLevelIndependentOfGlobalLevel(t *testing.T) {
	t.Setenv("TF_LOG", "")
	t.Setenv("TF_LOG_PROVIDER", "trace")

	captured := captureStderr(t, func() {
		_, stdWriter := stdWriterFor()
		wireStdLogger(t, stdWriter)
		if log.Writer() != io.Writer(stdWriter) {
			t.Fatal("test setup: standard logger is not wired to the fresh writer")
		}

		providerLogger := NewProviderLogger("")
		if got := providerLogger.GetLevel(); got != hclog.Trace {
			t.Errorf("expected provider logger level %s, got %s", hclog.Trace, got)
		}

		log.Printf("[TRACE] core message")
	})

	if captured != "" {
		t.Errorf("expected no output from the standard logger while the global level is off, got:\n%s", captured)
	}
}

func TestGlobalLogLevelCoreFallback(t *testing.T) {
	t.Setenv("TF_LOG", "")
	t.Setenv("TF_LOG_CORE", "trace")

	level, json := globalLogLevel()
	if level != hclog.Trace {
		t.Errorf("expected TF_LOG_CORE to set the global level to trace, got %s", level)
	}
	if json {
		t.Error("expected JSON format to be off")
	}
}

func TestStandardLogWriterSelection(t *testing.T) {
	t.Setenv("TF_LOG", "")
	logger, stdWriter := stdWriterFor()
	if got := standardLogWriter(logger, stdWriter); got != io.Discard {
		t.Fatalf("expected io.Discard when the global level is off, got %T", got)
	}

	sinksRegistered.Store(true)
	t.Cleanup(func() { sinksRegistered.Store(false) })
	if got := standardLogWriter(logger, stdWriter); got != io.Writer(stdWriter) {
		t.Fatalf("expected the hclog standard writer while a sink is registered, got %T", got)
	}
	sinksRegistered.Store(false)

	t.Setenv("TF_LOG", "trace")
	logger, stdWriter = stdWriterFor()
	if got := standardLogWriter(logger, stdWriter); got != io.Writer(stdWriter) {
		t.Fatalf("expected the hclog standard writer when logging is enabled, got %T", got)
	}
}

func TestProcessStdLoggerDiscardedWhenLoggingDisabled(t *testing.T) {
	if os.Getenv("TF_LOG") != "" || os.Getenv("TF_LOG_CORE") != "" {
		t.Skip("requires TF_LOG and TF_LOG_CORE to be unset")
	}
	if log.Writer() != io.Discard {
		t.Fatalf("standard logger output is %T, want io.Discard", log.Writer())
	}
}

func BenchmarkStdLogPrintfDisabled(b *testing.B) {
	if HCLogger().GetLevel() != hclog.Off {
		b.Skip("requires TF_LOG to be unset so that logging is disabled")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "registry.terraform.io/hashicorp/random")
	}
}

func BenchmarkStdLogPrintfEnabled(b *testing.B) {
	if HCLogger().GetLevel() != hclog.Trace {
		b.Skip("requires TF_LOG=trace")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "registry.terraform.io/hashicorp/random")
	}
}

func TestInvalidLogLevelStillEnablesLogging(t *testing.T) {
	t.Setenv("TF_LOG", "invalid-level-value")

	var logger hclog.Logger
	captured := captureStderr(t, func() {
		logger = newHCLogger("")
	})

	if !strings.Contains(captured, "[WARN] Invalid log level") {
		t.Errorf("expected a warning about the invalid log level, got:\n%s", captured)
	}
	if got := logger.GetLevel(); got != hclog.Trace {
		t.Errorf("expected an invalid log level to default to trace, got %s", got)
	}
}
