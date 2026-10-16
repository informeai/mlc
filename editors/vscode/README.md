# MLC para VS Code

Suporte à linguagem MLC (My Lang C):

- cores para palavras reservadas, tipos, funções, literais e comentários (gramática TextMate, mais as cores semânticas do servidor);
- erros ao digitar: sintaxe, tipos e posse de memória, marcados na linha, sem precisar salvar;
- formatação pelo `mlc fmt` (Format Document, ou `editor.formatOnSave`).

A extensão inicia `mlc lsp`. O `mlc` precisa estar no `PATH`, ou configurado em `mlc.path`.

## Instalar

```sh
cd editors/vscode
npm install --omit=dev
npx @vscode/vsce package --allow-missing-repository --skip-license
code --install-extension mlc-0.1.0.vsix
```
