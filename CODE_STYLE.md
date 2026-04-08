# Code Style

- name variables classes functions structs fields and all possible target language elements that ha a name, with meaninful but concise names, but you can use also 1/2/3 charaters names when there is no confusion in the namespace for example the classical i for a loop

- use prefixes and underscore when possible or where make sense to group multiple names togheter

- use snake_case when possible but only if there are no target language limits and the code will feel natural anyway

- you must comment in a succint way and always when there is ambiguity, be proportional to the context, for example a small function of 7 lines need only a not too long general comment about why is needed, about arguments and output, but if its doing something peculiar that needs attention comment it more profoundly

- place section comments like an horizontal line with a comment about ok there are all the utility functions and then keep the functions and code ordered to attain to the sections

- never grow the number of functions/classes/structs etc without a good reason, think if you can adapt an already available function adding one optional parameter while keeping backward compatibility or reuse something in a meaningful way that feels natural and make sense, it other word really add things if really needed

- prefer using jsonata (both on json and yaml) and xpath (for xml) third party libs instead of adding specific structs for this kind of data

- reason about the elegance and logic density in the code and always keep it in the middle both while writing new code or when rewiting a chunk of existing code

- about sql/jsonata/xpath or any other kind of data query reason about which query tactic will cover most of the requirements to merge multiple eventually required code paths in one, also taking in account a good balance for the performance

- don't exagerate about writing performant code at any cost, the requirement to have elegance and logic density in the middle is more important

- avoid too much nesting, but be very careful simply replacing a nested block with a function call that have the next deep levels of that nesting is not solving the problem it's making it worse, you need to think out of the box and find better solutions

- find patterns and avoid conditions every where is possible i dont want mechanical boilerplate at all: like `if a = "v" then b = "v"`

- avoid passing around objects references from function to function like http clients or golang contextes if not really required, i dont like best practices i like and absolutely want elegant readable code at all costs of performance or any best practices being negated

- use the right parser for the value, if you need to switch or do something based on a  complex meaninng of the value like for example on a url schema use the url parser and the simply switch on the url_parsed.schema, never ever do string splitting and strance convulted handling of the value for common formats like an url, and if the case is not commong write a parser function yourself and use it

- dont proliferate local or global variables passing from variable to variable the same thing withot a reason

- readability, the correct level of logical density, and code elegance are the top priority, all the rest is sacrificable

- keep error handling minimal

- centralize log and error reporting

- remember to use generators/async generators when the language support it dont use callbacks and events if a generator/yield/loop can be used

- use the stdlib of the language proficiently and with creativity, never write custom code to split a string that conforms to a path for example, but simply use some function in https://pkg.go.dev/path/filepath golang but most language have large stdlibs, explore diligently, also use regexps match or splits avoid string operations

- after parsing a value with the proper stdlib parser, the parsed object becomes the source of truth; do not keep deriving semantics from the raw string in parallel

- avoid hardcoding external data or paths or extensions, or anything, eventually as XDev about what to do

- 1/2/3 lines functions smells like bad modularization, avoid them be careful and inline the code

- also function with just one caller are bad modularization, keep the logic inline if so
