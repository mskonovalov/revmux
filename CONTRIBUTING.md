# Contributing to revmux

Thank you for your interest in contributing!

## Before You Start

### First-time contributors

If this is your first PR to revmux, please **open an issue first** describing what you plan to build and why. Wait for maintainer approval before writing code. The issue has to answer why: state the problem you actually hit and what does not work for you today. An issue that only specifies the proposed behavior, flag names, or defaults does not answer it, and neither does one written after the code. This saves everyone's time, some ideas may not fit the project's direction, and it is better to find out early than after investing effort into a PR.

### Check existing functionality

Before suggesting a feature or filing an issue, make sure the functionality does not already exist. Much of what looks missing is configuration rather than code: profiles, lenses and stage prompts are files, and `revmux init` materializes them locally so you can change what a review asks without touching Go. Run `revmux --help` for the flags, `revmux config` for the profiles, lenses and knobs that actually resolved, and read the README.

### Ideas and general questions

Use [Discussions](https://github.com/umputun/revmux/discussions), not Issues, for general questions, ideas, and brainstorming. Issues are for concrete, well-defined bugs or approved feature requests.

### Is it worth it?

Before submitting a PR, critically evaluate the tradeoff between what the feature adds and the code it introduces. A minor or edge-case improvement that inflates the codebase significantly is usually not a good tradeoff. Consider:

- **Does it belong in revmux or in the caller?** revmux runs a review and returns findings, nothing else. It does no scope detection, no git operations, no PR fetching, no issue handling and no source modification, and it has zero VCS dependency. All review context is written to disk by the caller and passed in as a task round. A change that makes revmux read a repository belongs in the caller, not here.
- **Is it prompt text rather than code?** A different question to ask the model is a lens or profile edit, not a new flag or a new pipeline stage.
- **Does it benefit most users or just a niche case?** Features that affect a handful of edge scenarios rarely justify the maintenance cost.
- **Is the code proportional to the value?** A 500-line PR for a feature used by 1% of users is a hard sell. Keep it simple, keep it small.

## Development Setup

1. Fork the repository
2. Clone your fork: `git clone https://github.com/your-username/revmux.git`
3. Create a feature branch: `git checkout -b feature-name`
4. Make your changes
5. Run tests: `make test`
6. Run linter: `make lint`
7. Format code: `make fmt`
8. Commit your changes: `git commit -am 'Add feature'`
9. Push to the branch: `git push origin feature-name`
10. Submit a pull request

No test may spawn a real `claude` or `codex` process. Subprocess behavior is covered with a mocked `CommandRunner` and recorded fixtures.

The two skill trees under `.claude-plugin/` and `plugins/codex/` carry duplicate copies of `references/` and `scripts/` on purpose, since a plugin has to be self-contained once installed. A change to one must be made to the other, and `make check-plugins` verifies they agree. CI runs it, so a one-sided edit fails the build.

## Code Style

Please follow the code style guidelines in [CLAUDE.md](CLAUDE.md), and the per-subsystem notes in `.claude/rules/`.

## Issues and PRs

Every issue and PR must clearly describe:

1. **What is the problem?** What exactly is broken, missing, or inconvenient? Be specific. "It would be nice to have X" is not a problem statement.
2. **How does this solve it?** Explain why this particular approach is the right fix and how it addresses the root cause.

PRs without a clear problem statement will be closed. If you cannot articulate the problem, the solution is probably not needed.

## Pull Request Process

1. Update the README.md with details of changes if applicable. A change to a flag, a roster key, an exit code or the JSON shape belongs in the README and in both skill trees, in the same commit as the code.
2. The PR should work for all configured platforms and pass all tests
3. PR will be merged once it receives approval from maintainers
