package executor_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
)

func TestClaude_Authentication(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		want         bool
		wantErr      bool
	}{
		{"logged in", `{"loggedIn":true}`, true, false},
		{"logged out", `{"loggedIn":false}`, false, false},
		{"invalid response", `not JSON`, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakeRunner("emit", writeFixture(t, []byte(tt.output)))
			provider := executor.NewClaude(runner, executor.Opts{})
			got, err := provider.Authenticated(t.Context())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			calls := runner.CommandCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, "claude", calls[0].Name)
			assert.Equal(t, []string{"--setting-sources", "project", "auth", "status", "--json"}, calls[0].Args)
		})
	}
}

func TestClaude_Authentication_nonzeroStatusIsNotAuthenticated(t *testing.T) {
	runner := fakeRunner("fail", writeFixture(t, []byte(`{"loggedIn":true}`)))
	provider := executor.NewClaude(runner, executor.Opts{})
	loggedIn, err := provider.Authenticated(t.Context())
	require.Error(t, err)
	assert.False(t, loggedIn)
}

func TestCodex_Authentication(t *testing.T) {
	for _, tt := range []struct {
		name, mode, output string
		want               bool
		wantErr            bool
	}{
		{"logged in", "emit", "Logged in using ChatGPT", true, false},
		{"logged out", "fail", "Not logged in", false, false},
		{"unexpected failure", "fail", "other failure", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
				t.Setenv(key, "")
			}
			runner := fakeRunner(tt.mode, writeFixture(t, []byte(tt.output)))
			provider := executor.NewCodex(runner, executor.Opts{})
			got, err := provider.Authenticated(t.Context())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			calls := runner.CommandCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, "codex", calls[0].Name)
			assert.Equal(t, []string{"login", "status"}, calls[0].Args)
		})
	}
}

func TestCodex_Authentication_envCredentialTakesPrecedence(t *testing.T) {
	for _, key := range []string{"CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			for _, other := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
				t.Setenv(other, "")
			}
			t.Setenv(key, "configured-credential")
			runner := fakeRunner("emit", writeFixture(t, []byte("Logged in using ChatGPT")))
			provider := executor.NewCodex(runner, executor.Opts{})
			loggedIn, err := provider.Authenticated(t.Context())
			require.NoError(t, err)
			assert.True(t, loggedIn)
			assert.Empty(t, runner.CommandCalls(), "stored login status does not check the effective exec credential")
		})
	}
}

func TestCodex_Authentication_openaiApiKeyAloneDoesNotAuthenticate(t *testing.T) {
	for _, key := range []string{"CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("OPENAI_API_KEY", "configured-credential")
	provider := executor.NewCodex(fakeRunner("fail", writeFixture(t, []byte("Not logged in"))), executor.Opts{})
	loggedIn, err := provider.Authenticated(t.Context())
	require.NoError(t, err)
	assert.False(t, loggedIn)
}

func TestCodex_Authentication_loggedOutOnStderr(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
		t.Setenv(key, "")
	}
	runner := fakeRunner("fail", writeFixture(t, nil), writeFixture(t, []byte("Not logged in")))
	provider := executor.NewCodex(runner, executor.Opts{})
	loggedIn, err := provider.Authenticated(t.Context())
	require.NoError(t, err)
	assert.False(t, loggedIn)
}

func TestAuthenticators_Login(t *testing.T) {
	for _, tt := range []struct {
		name, command string
		args          []string
		provider      func(executor.CommandRunner) executor.Authenticator
	}{
		{"claude", "claude", []string{"auth", "login"}, func(r executor.CommandRunner) executor.Authenticator {
			return executor.NewClaude(r, executor.Opts{})
		}},
		{"codex", "codex", []string{"login"}, func(r executor.CommandRunner) executor.Authenticator {
			return executor.NewCodex(r, executor.Opts{})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakeRunner("emit", writeFixture(t, []byte("Login complete")))
			provider := tt.provider(runner)
			require.ErrorContains(t, provider.Login(t.Context(), nil), tt.command)
			var output bytes.Buffer
			terminal := struct {
				io.Reader
				io.Writer
			}{Reader: strings.NewReader(""), Writer: &output}
			require.NoError(t, provider.Login(t.Context(), terminal))
			assert.Equal(t, "Login complete", output.String())
			calls := runner.CommandCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, tt.command, calls[0].Name)
			assert.Equal(t, tt.args, calls[0].Args)
		})
	}
}
