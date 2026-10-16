// Extensão do VS Code para o MLC: inicia o `mlc lsp` e liga o editor a ele.
// As cores básicas vêm da gramática TextMate (syntaxes/); os erros ao digitar,
// as cores semânticas e a formatação vêm do servidor.
const vscode = require("vscode");
const { LanguageClient } = require("vscode-languageclient/node");

let client;

function activate(context) {
  const command = vscode.workspace.getConfiguration("mlc").get("path") || "mlc";
  client = new LanguageClient(
    "mlc",
    "MLC",
    { command, args: ["lsp"] },
    { documentSelector: [{ scheme: "file", language: "mlc" }] }
  );
  client.start().catch((err) => {
    vscode.window.showErrorMessage(
      `MLC: não foi possível iniciar "${command} lsp" (${err.message}). Ajuste a configuração mlc.path.`
    );
  });
  context.subscriptions.push({ dispose: () => client && client.stop() });
}

function deactivate() {
  return client ? client.stop() : undefined;
}

module.exports = { activate, deactivate };
