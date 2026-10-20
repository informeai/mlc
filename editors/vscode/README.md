# MLC for VS Code

Support for the MLC language (My Lang C):

- colors for keywords, types, functions, literals and comments (a TextMate grammar, plus the server's semantic colors);
- errors as you type: syntax, types and memory ownership, marked on the line, without saving;
- formatting through `mlc fmt` (Format Document, or `editor.formatOnSave`).

The extension starts `mlc lsp`. `mlc` must be in the `PATH`, or configured in `mlc.path`.

## Install

```sh
cd editors/vscode
npm install --omit=dev
npx @vscode/vsce package --allow-missing-repository --skip-license
code --install-extension mlc-0.1.0.vsix
```
