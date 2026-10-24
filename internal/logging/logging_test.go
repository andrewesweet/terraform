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

// captureStderr replaces os.Stderr with a pipe for the duration of fn, and
// returns whatever was written to it. Loggers created inside fn will send
// their output to the pipe, because newHCLogger reads os.Stderr when it is
// called.
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

// wireStdLogger points the standard library logger at w, the same way the
// logging package's init function does, and restores the previous wiring when
// the test finishes.
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

// stdWriterFor builds a global hclog logger from the current environment and
// returns the standard library writer derived from it, matching the wiring in
// the package's init function.
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

// The standard writer feeds the logger's underlying implementation, not the
// InterceptLogger wrapper, so messages arriving through the standard logger
// never reach registered sinks, while direct hclog API calls do (sinks are
// dispatched independently of the global level, which is how crash logs
// capture anything at all when TF_LOG is unset). This pins that routing so
// that routing the standard logger elsewhere cannot silently change what
// crash logs capture.
func TestStdLoggerMessagesDoNotReachSinks(t *testing.T) {
	t.Run("logging disabled", func(t *testing.T) {
		t.Setenv("TF_LOG", "")
		testStdLoggerSinkRouting(t)
	})
	t.Run("logging enabled", func(t *testing.T) {
		t.Setenv("TF_LOG", "trace")
		testStdLoggerSinkRouting(t)
	})
}

func testStdLoggerSinkRouting(t *testing.T) {
	t.Helper()

	sinkPath := filepath.Join(t.TempDir(), "sink.log")
	f, err := os.OpenFile(sinkPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		t.Fatalf("open sink file: %s", err)
	}

	captureStderr(t, func() {
		logger := newHCLogger("")
		intercept, ok := logger.(hclog.InterceptLogger)
		if !ok {
			t.Fatal("global logger is not an InterceptLogger")
		}
		intercept.RegisterSink(hclog.NewSinkAdapter(&hclog.LoggerOptions{
			Level:  hclog.Trace,
			Output: f,
		}))

		_, stdWriter := stdWriterFor()
		wireStdLogger(t, stdWriter)
		if log.Writer() != io.Writer(stdWriter) {
			t.Fatal("test setup: standard logger is not wired to the fresh writer")
		}

		log.Printf("[DEBUG] hello %s", "world") // through the standard logger
		logger.Info("direct call")              // through the hclog API
	})
	f.Close()

	data, err := os.ReadFile(sinkPath)
	if err != nil {
		t.Fatalf("read sink file: %s", err)
	}
	if strings.Contains(string(data), "hello world") {
		t.Errorf("standard logger message unexpectedly reached the sink:\n%s", data)
	}
	if !strings.Contains(string(data), "direct call") {
		t.Errorf("sink stopped capturing direct hclog calls:\n%s", data)
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

// When logging is disabled the standard logger must be routed to exactly
// io.Discard, because Go's log package applies its formatting fast path only
// when the writer is io.Discard. When logging is enabled the hclog standard
// writer must be used as before.
func TestStandardLogWriterSelection(t *testing.T) {
	t.Setenv("TF_LOG", "")
	logger, stdWriter := stdWriterFor()
	if got := standardLogWriter(logger, stdWriter); got != io.Discard {
		t.Fatalf("expected io.Discard when the global level is off, got %T", got)
	}

	t.Setenv("TF_LOG", "trace")
	logger, stdWriter = stdWriterFor()
	if got := standardLogWriter(logger, stdWriter); got != io.Writer(stdWriter) {
		t.Fatalf("expected the hclog standard writer when logging is enabled, got %T", got)
	}
}

// The test binary runs without TF_LOG set, so the init wiring in this package
// has routed the process-wide standard library logger. Go's log package only
// applies its fast path when the writer is exactly io.Discard, so assert
// identity rather than behaviour.
func TestProcessStdLoggerDiscardedWhenLoggingDisabled(t *testing.T) {
	if os.Getenv("TF_LOG") != "" || os.Getenv("TF_LOG_CORE") != "" {
		t.Skip("requires TF_LOG and TF_LOG_CORE to be unset")
	}
	if log.Writer() != io.Discard {
		t.Fatalf("standard logger output is %T, want io.Discard", log.Writer())
	}
}

// BenchmarkStdLogPrintfDisabled times a representative log.Printf call with
// logging disabled (TF_LOG unset), which is how terraform runs in automation
// and tests. The benchmark guards its own precondition so that it cannot
// silently measure the wrong wiring.
func BenchmarkStdLogPrintfDisabled(b *testing.B) {
	if HCLogger().GetLevel() != hclog.Off {
		b.Skip("requires TF_LOG to be unset so that logging is disabled")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "registry.terraform.io/hashicorp/random")
	}
}

// BenchmarkStdLogPrintfEnabled times the same call with logging enabled at
// TRACE level to show that the enabled path is unchanged. Run the test binary
// with TF_LOG=trace (and stderr redirected) for this benchmark.
func BenchmarkStdLogPrintfEnabled(b *testing.B) {
	if HCLogger().GetLevel() != hclog.Trace {
		b.Skip("requires TF_LOG=trace")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.Printf("[TRACE] LoadSchemas: retrieving schema for provider type %q", "registry.terraform.io/hashicorp/random")
	}
}

// An invalid TF_LOG value must keep logging enabled at TRACE level (the
// warning is printed to stderr), so it must not take any logging-disabled
// fast path.
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
