package executor_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
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
			require.Len(t, calls, 2)
			assert.Equal(t, "codex", calls[0].Name)
			assert.Equal(t, []string{"doctor", "--json"}, calls[0].Args)
			assert.Equal(t, []string{"login", "status"}, calls[1].Args)
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

func TestCodex_Authentication_customProviderWithoutOpenAIAuth(t *testing.T) {
	for _, tt := range []struct {
		name, doctor string
		want         bool
		wantCalls    [][]string
	}{
		{"custom provider", codexDoctor(false), true, [][]string{{"doctor", "--json"}}},
		{"OpenAI provider", codexDoctor(true), false, [][]string{{"doctor", "--json"}, {"login", "status"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
				t.Setenv(key, "")
			}
			status := writeFixture(t, []byte("Not logged in"))
			doctor := writeFixture(t, []byte(tt.doctor))
			runner := &mocks.CommandRunnerMock{CommandFunc: func(_ context.Context, _ string, args ...string) *exec.Cmd {
				if args[0] == "doctor" {
					return helperCmd("fail", doctor) // unrelated diagnostics can make doctor exit non-zero
				}
				return helperCmd("fail", status)
			}}
			provider := executor.NewCodex(runner, executor.Opts{})
			loggedIn, err := provider.Authenticated(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.want, loggedIn)
			calls := runner.CommandCalls()
			args := make([][]string, 0, len(calls))
			for _, call := range calls {
				args = append(args, call.Args)
			}
			assert.Equal(t, tt.wantCalls, args)
		})
	}
}

func TestCodex_Authentication_gatewayCheck(t *testing.T) {
	for _, tt := range []struct {
		name, mode string
		want       bool
	}{
		{"valid credentials", "emit", true},
		{"expired credentials", "fail", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
				t.Setenv(key, "")
			}
			doctor := writeFixture(t, []byte(codexDoctor(false)))
			check := writeFixture(t, nil)
			runner := &mocks.CommandRunnerMock{CommandFunc: func(_ context.Context, name string, _ ...string) *exec.Cmd {
				if name == "codex" {
					return helperCmd("emit", doctor)
				}
				return helperCmd(tt.mode, check)
			}}
			provider := executor.NewCodex(runner, executor.Opts{GatewayCheck: "ai-gateway token"})
			loggedIn, err := provider.Authenticated(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.want, loggedIn)
			calls := runner.CommandCalls()
			require.Len(t, calls, 2, "the stored ChatGPT login is never consulted")
			assert.Equal(t, "ai-gateway", calls[1].Name)
			assert.Equal(t, []string{"token"}, calls[1].Args)
		})
	}
}

func TestCodex_Login_gateway(t *testing.T) {
	doctor := writeFixture(t, []byte(codexDoctor(false)))
	login := writeFixture(t, []byte("Logged in"))
	runner := &mocks.CommandRunnerMock{CommandFunc: func(_ context.Context, name string, _ ...string) *exec.Cmd {
		if name == "codex" {
			return helperCmd("emit", doctor)
		}
		return helperCmd("emit", login)
	}}
	require.ErrorContains(t, executor.NewCodex(runner, executor.Opts{}).Login(t.Context(), nil), "gateway-login")

	provider := executor.NewCodex(runner, executor.Opts{GatewayLogin: "ai-gateway login"})
	require.ErrorContains(t, provider.Login(t.Context(), nil), "ai-gateway login")
	var output bytes.Buffer
	terminal := struct {
		io.Reader
		io.Writer
	}{Reader: strings.NewReader(""), Writer: &output}
	require.NoError(t, provider.Login(t.Context(), terminal))
	assert.Equal(t, "Logged in", output.String())
	calls := runner.CommandCalls()
	last := calls[len(calls)-1]
	assert.Equal(t, "ai-gateway", last.Name)
	assert.Equal(t, []string{"login"}, last.Args)
}

// the array-valued detail is the shape a real doctor report carries in unrelated checks
func codexDoctor(requiresOpenAIAuth bool) string {
	return fmt.Sprintf(`{"checks":{"auth.credentials":{"details":{"model provider requires OpenAI auth":"%t"}},`+
		`"state.rollout_db_parity":{"details":{"rollout DB missing active sample":["id"]}}}}`, requiresOpenAIAuth)
}

func TestAuthenticators_Login(t *testing.T) {
	for _, tt := range []struct {
		name, command string
		args          []string
		calls         int
		provider      func(executor.CommandRunner) func(context.Context, io.ReadWriter) error
	}{
		{"claude", "claude", []string{"auth", "login"}, 1, func(r executor.CommandRunner) func(context.Context, io.ReadWriter) error {
			return executor.NewClaude(r, executor.Opts{}).Login
		}},
		// each codex login first asks doctor whether an external provider owns the credentials
		{"codex", "codex", []string{"login"}, 3, func(r executor.CommandRunner) func(context.Context, io.ReadWriter) error {
			return executor.NewCodex(r, executor.Opts{}).Login
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakeRunner("emit", writeFixture(t, []byte("Login complete")))
			login := tt.provider(runner)
			require.ErrorContains(t, login(t.Context(), nil), tt.command)
			var output bytes.Buffer
			terminal := struct {
				io.Reader
				io.Writer
			}{Reader: strings.NewReader(""), Writer: &output}
			require.NoError(t, login(t.Context(), terminal))
			assert.Equal(t, "Login complete", output.String())
			calls := runner.CommandCalls()
			require.Len(t, calls, tt.calls)
			assert.Equal(t, tt.command, calls[tt.calls-1].Name)
			assert.Equal(t, tt.args, calls[tt.calls-1].Args)
		})
	}
}
