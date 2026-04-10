# Terms

- `XProject` MUST mean the current project.
- `XDev` MUST mean the developer instructing work on `XProject`.
- `XAgent` MUST mean the AI agent developing `XProject`.
- `XProjectUser` MUST mean the final released-project user.

# Rules

- `XAgent` MUST initialize git if the project is not already a git repository.
- `XAgent` MUST keep `.gitignore` present and meaningfully updated for this project.
- `XAgent` MUST create a local git commit on the current branch at the end of every prompt-round if local changes were made.
- `XAgent` MUST explicitly notify `XDev` if something goes wrong.
- `XAgent` MUST be technical and concise in answers and comments to `XDev`.
- `XAgent` MUST answer in the language used by `XDev`.
- `XAgent` MUST keep established programming terms in their natural form when literal translation would be misleading.
- `XAgent` MUST keep code comments, `README.md`, and other project user-facing text in English unless `XDev` explicitly requests otherwise.

- `DESIDERATA.md` MUST be treated as the high-level target feature set and MUST NOT be edited by `XAgent`.
- Before implementing anything derived from `DESIDERATA.md`, `XAgent` MUST compare the logical differences between `DESIDERATA.md` and `README.md`.
- Before implementing anything derived from those differences, `XAgent` MUST ask `XDev` for confirmation.
- `README.md` MUST be treated as the end-user-facing description of currently implemented behavior.
- Whenever final user-facing behavior changes, `XAgent` MUST update `README.md` in the best existing section or create a new one, and the update MUST be simple, synthesized, and non-redundant.

- `XAgent` MUST follow `CODE_STYLE.md` for every written or rewritten line of code.
- `XAgent` MUST follow `CODE_DESIGN.md` for every code-structure decision.
- `CODE_DESIGN.md` MUST be treated as authoritative for signatures, structs, classes, functions, methods, and structural intent.
- `XAgent` MUST NOT invent or add structural elements that are not present in `CODE_DESIGN.md`.
- If `XAgent` determines that `CODE_DESIGN.md` cannot be followed, `XAgent` MUST explain why, MUST propose the needed design change to `XDev`, MUST wait for `XDev` to update `CODE_DESIGN.md`, and MUST NOT edit `CODE_DESIGN.md` directly.

- `XAgent` MUST keep source files under `src/`.
- `XAgent` MUST keep build outputs under `bin/`.
- `XAgent` MUST keep the self-contained build system under `build/`.
- `XAgent` MUST keep the main source file as `src/main.<ext>` according to the language, for example `src/main.go`.
- `XAgent` MUST NOT proliferate source files and MUST keep code in the main source file unless `CODE_DESIGN.md` explicitly requires otherwise.
- `XAgent` MUST keep tests under `tests/`.
- Build and test subsystems MUST store cache and ephemeral data in `build_cache/` and `tests_cache/` respectively.

- Tooling and build systems MUST be standalone, relocatable on the filesystem, platform-independent when reasonably possible, and cold-bootstrappable.
- Tooling and build systems MUST NOT require manual dependency download, manual extraction, or manual configuration by `XDev` or `XProjectUser`.
- Build entrypoints SHOULD be small scripts such as `build.bat`, `build.sh`, or equivalent, able to bootstrap their requirements automatically.
- `XAgent` SHOULD use the latest stable runtimes, compilers, and tools.
- `XAgent` SHOULD prefer Ubuntu for WSL and GitHub Actions.
- Text, script, and source files SHOULD use LF line endings regardless of host OS, unless another format is strictly required.

- `XAgent` MUST keep the test suite updated whenever a feature or code change is significant.
- `XAgent` MUST NOT run tests at every prompt-round by default.
- `XAgent` MUST run tests when something important changed.
- When running tests, `XAgent` MUST run only the tests relevant to the current development OS.
- For CLI projects, tests MUST treat the executable interface as the source of truth and MUST be conceived primarily as CLI black-box tests.
- For interoperability with public registries or services, tests MUST hit real public endpoints and real published artifacts, not simulated local equivalents.
- Such tests SHOULD use meaningful, mainstream, likely-stable examples and SHOULD optimize for low bandwidth and execution time.
- `XAgent` MUST NOT add code, features, flags, environment variables, or behavior only to simplify, fake, or mock tests unless `CODE_DESIGN.md` explicitly requires it.

- `XAgent` MUST keep the CI/workflow pipeline updated so it can create release artifacts for Linux x64 and Windows x64.
- The CI/workflow pipeline MUST reuse the existing build system.
- The CI/workflow pipeline MUST permit manual triggering.
- `XAgent` SHOULD avoid third-party GitHub Actions, including official GitHub Actions, when shell commands or `gh` can reasonably replace them.
- `XAgent` SHOULD prefer Linux GitHub runners and cross-compilation for other targets when practical; otherwise it SHOULD use multiple runners in the same workflow.

- `XAgent` MUST use and manage `gh` for repository, branch, push, workflow, tag, release, and metadata operations when those actions are needed.
- If `gh` authentication is missing or broken, `XAgent` MUST ask `XDev` to complete the authentication flow and MUST provide the URL/code flow needed to initialize the token with the required scopes.

- If the repository has a configured GitHub remote and `gh` is authenticated, then after every significant user-facing or release-relevant change `XAgent` MUST push the current branch.
- If the repository has a configured GitHub remote and `gh` is authenticated, then after every significant user-facing or release-relevant change `XAgent` MUST update the GitHub repository description if the project scope or message changed materially.
- If the repository has a configured GitHub remote and `gh` is authenticated, then after every significant user-facing or release-relevant change `XAgent` MUST create or update the release tag and the corresponding GitHub Release.
- In such cases, `XAgent` MUST state in the final response exactly which push, tag, and release were created.
- Significant user-facing or release-relevant changes MUST include at least new features, new CLI flags, behavior changes visible to `XProjectUser`, support for new platforms, formats, or protocols, and fixes to previously broken advertised behavior.
- `XAgent` MUST NOT treat push, tag, or release as optional when the previous conditions are met.
- If a GitHub remote is missing, `gh` is not authenticated, or push, tag, or release creation fails, `XAgent` MUST explicitly report the exact blocking command or error in the same prompt-round final response.
- If no significant user-facing or release-relevant change happened, `XAgent` MUST NOT create a tag or release.
- Missing a required push, tag, or release when the previous conditions are met MUST be treated as a rule violation.

- If the current development environment is Windows and Linux execution or testing is needed, `XAgent` MUST use WSL Ubuntu.
- If WSL or the Ubuntu distro is missing and Linux execution is needed, `XAgent` MUST install it.
- If privileged work is needed inside WSL, `XAgent` MUST use root first and SHOULD switch to a non-root user afterward when appropriate.

- If `XAgent` needs extra tools that are not available in the current environment, `XAgent` MUST install them in portable form under `.agents_tools/`.
- `XAgent` MUST track such tools in `AGENTS_TOOLS.md`.
- Such `AGENTS_TOOLS.md` tools MUST be used only by `XAgent` for the agentic workflow support and MUST NOT become part of the project itself nor the build or test system the can be the same but are totally sperated and for use ONLY in exploration ananlysys code search etc by XAgent.
- Examples of such tools MAY include `rg`, `osquery`, `xmake`, or similar utilities.

- `XAgent` MUST NOT modify, delete, or rewrite `AGENTS.md`, `CLAUDE.md`, `CODE_STYLE.md`, `CODE_DESIGN.md`, or `DESIDERATA.md` unless `XDev` explicitly requests that exact change.
