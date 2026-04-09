# Code Style

- Names for variables, functions, structs, classes, fields, and other language elements MUST be meaningful and concise.
- Very short names SHOULD be used only when they are conventional and unambiguous in local scope, for example `i` in a loop.
- Naming SHOULD use grouping patterns such as prefixes or underscores when that improves clarity.
- `snake_case` SHOULD be used when the target language allows it and the result still feels natural.

- Comments MUST be concise and MUST be written whenever the code would otherwise be ambiguous.
- Comment depth SHOULD be proportional to complexity.
- Small code blocks SHOULD receive a short general comment when needed.
- Peculiar or surprising logic SHOULD receive deeper explanation when needed.
- Large files SHOULD use section comments to group related code and keep ordering clear.

- New functions, classes, structs, globals, or similar abstractions MUST NOT be added without a good reason.
- Before adding a new abstraction, `XAgent` SHOULD first evaluate whether an existing one can be extended naturally and readably.
- Tiny 1-3 line functions SHOULD be treated as a code smell and SHOULD be inlined when they add no real abstraction.
- Functions with a single caller SHOULD be treated as a code smell and SHOULD be inlined when that improves readability.
- Local or global variables MUST NOT be proliferated when they only relay the same value without adding meaning.

- Code MUST optimize first for readability, correct logical density, and elegance.
- Performance MUST NOT be pursued at the cost of readability, correct logical density, or elegance.
- Code SHOULD stay in the middle ground between over-fragmented boilerplate and overly dense obscurity.
- Nesting SHOULD be minimized.
- Replacing a nested block with a trivial helper that only hides the same nesting MUST NOT be treated as a valid simplification.
- `XAgent` SHOULD prefer structurally better solutions over mechanical extraction.
- Patterns and general rules SHOULD be preferred over repetitive conditionals and mechanical boilerplate.

- Error handling SHOULD be minimal.
- Logging and error reporting SHOULD be centralized.

- The correct parser MUST be used for structured values.
- For common formats, `XAgent` MUST use the stdlib or the proper parser/library instead of manual string splitting or convoluted ad-hoc handling.
- If a decision depends on a complex value meaning, `XAgent` MUST parse the value first and MUST reason from the parsed representation.
- After parsing, the parsed object MUST become the source of truth, and `XAgent` MUST NOT derive the same semantics again from the raw string in parallel.
- If the format is uncommon and no good parser exists, `XAgent` SHOULD write a parser function and then use it consistently.
- The stdlib MUST be used proficiently and creatively.
- Regex SHOULD be preferred over manual string operations when regex is the natural tool.

- `XAgent` SHOULD prefer JSONata for JSON/YAML querying and XPath for XML querying instead of adding many specific structs when that improves clarity and coverage.
- For SQL, JSONata, XPath, and similar query systems, `XAgent` SHOULD choose query strategies that cover the largest useful set of cases with a good balance of clarity and performance.
- Query design SHOULD prefer merging multiple code paths into a smaller number of expressive paths when that remains readable.

- `XAgent` MUST NOT pass around heavyweight references such as HTTP clients or Go contexts unless they are truly required.
- `XAgent` SHOULD prefer direct, readable control flow over fashionable patterns or best-practice cargo culting.
- If the language supports generators or async generators naturally, `XAgent` SHOULD prefer them over callbacks or event-style APIs when they make the code clearer.

- External data, paths, extensions, and similar assumptions MUST NOT be hardcoded without a good reason.
- If such a choice is required and unclear, `XAgent` SHOULD ask `XDev`.

- For Go projects, the following libraries SHOULD be preferred when needed:
- JSONata: `https://github.com/RecoLabs/gnata`
- Archive extraction: `https://github.com/mholt/archives`
- YAML: `https://github.com/yaml/go-yaml`
- XPath: `https://github.com/speedata/goxpath`
- Doublestar glob: `https://github.com/bmatcuk/doublestar`

- `XAgent` MUST ask `XDev` before adding any additional third-party dependency.
- For Python runtime and package management, `XAgent` MUST use `uv`.
- For JavaScript or TypeScript, `XAgent` MUST use Bun, and SHOULD use Deno as fallback if needed.

- Fewer lines of code SHOULD be preferred when readability, elegance, and logical density are preserved.
