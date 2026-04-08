# Entities referenced in this document

- XProject is the current project we are working on

- XDev is the user prompiting requests to develop XProject

- XAgent is the ai agent for which these rules are written for which AiDev prompt to in order to develop the XProject

- XProjectUser, its the user that will use the project while released

# Agent rules

## All the following rules are important regardless the order of apparence

- At the end of each prompt-round commit changes in the current branch if something goes wrong notify the XDev

- Whenever final user facing behavior change is made, always update `README.md` to reflect it in a simple systehetized non redundant way in the best section available or create a new section

- whenever a feature or code change it's significative keep the test suite updated

- its imperative you follow CODE_STYLE.md for any line of code written or rewritten changed

- its imperative you follow CODE_DESIGN.md for any addition or change in the code structure, there you will find, in pseudocode, the signatures of classes, structs, functions, methods, and comments around all this code elements them to follow, never expand or dream different solutions from said design, if you find that its absolutely impossible to attain to the design specified in CODE_DESIGN.md and you need to add a new function or or struct or anything not present in the design than explain why and propose a change to XDev, but never chage the CODE_DESIGN.md file, if XDev will like your proposed change in design XDev will change itself manually in any way he likes and will notify you so you can reread CODE_DESIGN.md to proceed, and the dev loop restarts as written above

- Be as thecnical and concise as you can in your answers and comments, XDev its a serious developer and its not your friend its your boss/coworker

- if prompts from XDev are in a specific languare answer in that language, beting careful to translate only the words that make senses in the target language, for example if the target language is italian you shoud not traslate afunction "signature" to "firma" but keep it as is, it's also important to remember that differently from comunications targeted to XDev all the code comments readme and other output shoud be in english if not expressely said otherwise

- keep any required tooling and build system standalone platform independent relocatable on the fs (using relative paths when possible or anything else), and cold bootstrappable no manual dependencies to download configure extract etc etc for the end user and the developer, for example a single build.bat and/or .ps1 and/or .sh that download all the requirements and build all

- keep the source files inside a src subdirectory in project root

- keep the build output binaries in the bin subdirectory in project root

- keep the self contained build system in the build subdirectory in project root

- keep the main source file in src/main.c for c for example or src/main.go for golang

- dont proliferate source files use only the main source file until specified otherwise in CODE_DESIGN.md

- keep an updated test suite the tests subdirectory in project root
