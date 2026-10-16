" Cores para MLC (My Lang C). O mlc lsp acrescenta as cores semânticas.
if exists("b:current_syntax")
  finish
endif

syn keyword mlcKeyword     import fn struct enum const var pub extern interface impl
syn match   mlcKeyword     "\<impl\s\+\zsauto\>"
syn keyword mlcConditional if else match
syn keyword mlcRepeat      for in
syn keyword mlcStatement   return break continue
syn keyword mlcType        int int8 int16 int32 int64 uint8 uint16 uint32 uint64 byte float32 float64 bool string
syn keyword mlcConstraint  any comparable ordered number integer Self
syn keyword mlcConstant    true false nil
syn keyword mlcBuiltin     alloc free realloc len take replace drain

syn match  mlcComment   "//.*$" contains=@Spell
syn region mlcString    start=+"+ skip=+\\.+ end=+"\|$+ contains=mlcEscape
syn match  mlcChar      "'\(\\.\|[^'\\]\)'" contains=mlcEscape
syn match  mlcEscape    "\\[ntr0\\\"']" contained
syn match  mlcNumber    "\<\d\+\(\.\d\+\)\=\([eE][+-]\=\d\+\)\=\>"
syn match  mlcFunction  "\<\h\w*\ze\s*("
syn match  mlcTypeName  "\<\u\l\w*\>"
syn match  mlcConstName "\<\u[A-Z0-9_]\+\>"
syn match  mlcOperator  "=>\|:=\|&&\|||\|[-+*/%=!<>&?]"

hi def link mlcKeyword     Keyword
hi def link mlcConditional Conditional
hi def link mlcRepeat      Repeat
hi def link mlcStatement   Statement
hi def link mlcType        Type
hi def link mlcConstraint  Type
hi def link mlcTypeName    Type
hi def link mlcConstant    Boolean
hi def link mlcConstName   Constant
hi def link mlcBuiltin     Function
hi def link mlcFunction    Function
hi def link mlcComment     Comment
hi def link mlcString      String
hi def link mlcChar        Character
hi def link mlcEscape      SpecialChar
hi def link mlcNumber      Number
hi def link mlcOperator    Operator

let b:current_syntax = "mlc"
