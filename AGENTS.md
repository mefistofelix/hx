# Entities referenced in this document

- XProject is the current project we are working on

- XDev is the user prompiting requests to develop XProject

- XAgent is the ai agent for which these rules are written for which AiDev prompt to in order to develop the XProject

- XProjectUser, its the user that will use the project while released

# Agent rules

## All the following rules are important regardless the order of apparence

- if not initiliazed init git

- initialize and update accordinly to other rules and in a meaningful way a gitignore file for this project

- At the end of each prompt-round commit changes in the current branch if something goes wrong notify the XDev

- DESIDERATA.md if foundamental and repesents more or less verbosely the destination/finel feature set we want to implement in the project/code, this file can change in the project developement, but you should never touch it only XDev wil eventually update id to inform you about a new desiderata, you must check the logical not raw diffrences between DESIDERATA.md and README.md to discover what needs to be implemented, beforse starting to implementn something however always ask XDev for confirmation

- Whenever final user facing behavior change is made, always update `README.md` to reflect it in a simple systehetized non redundant way in the best section available or create a new section, README.md reflects and documents for the final user the current features implemented in the project at the current stage of development

- also commit and push and update the github repo description (with a catchy summary) and tag/trigger a release when something important changes and we have a github repo linked to the project

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

- when the feature is about interoperability with external services or registries, tests are expected to hit real public endpoints and real published artifacts, not simulated local equivalents, choose significative examples but optimize for low badwith and execution time, also if artifacts or endpoint are required from the internet choose mainstream things that are exepected to last

- run test only when something important is changed not at every prompt round, and when running tests run only the ones for the current development env os

- build and tests subsystems will save cache/ephemeral data respectively to build_cache and tests_cache

- never and ever add features or any line of code or enviroement variable or cli argument in the code for the purpose to simplify or mock the test suite, do that only if asked explicitly in CODE_DESIGN.md

- never ever change delete or modify agents.md claude.md code_style.md or code_design.md desiderata.md

- create and keep updated also the workflow/ci pipeline to create release artifacts for linux and windows x64, reusing the build system we already have and avoiding importing thirdparty actions even the GitHub official actions should be avoided (for example use gh commands to checkout and not external thirdparty checkoutactions), also permit the workflow to be manually triggered

- if possible prefer a linux machine for the github action and crosscompile to create the release for other platforms, if not possible use different machines in the seme workflow

- install and handle gh commands to update/commit/create/push/handle workflows/delete repos/create repos/change repo descriptions etc, obtain a gh token with the required security scopes once or if something goes wrong by prompting to XDev the url and the code to allow the gh token to be initialized

- if the current developement env is windows and you need to execute or test something in a linux env use windows wsl ubuntu, eventually install the wsl subsystem and distro if missing, also use the root flag in wsl to enter the subsystem to install packages etc, you can always switch to a non root user from root using su

- prefer using latest versions of runtimes, compilers and tools used

- prefer ubuntu linux distro both for wsl and github actions

- prefer linux line ending for text, script and source files independently from the current dev env os, fallback to other line endings only if required

- for cli projects, the source of truth for testing is the executable interface, not internal language-level test structure; tests must be conceived and organized as cli black-box tests first

- if you happens to needs tool that are not available in the current dev env to work better, download them in a portable mode inside a project subfolder .agents_tools and use them. es. ripgrep osquery also track them in AGENTS_TOOLS.md for your convenience and future memory
