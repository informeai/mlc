// VS Code extension for MLC: starts `mlc lsp` and connects the editor to it.
// The basic colors come from the TextMate grammar (syntaxes/); the errors as you type,
// the semantic colors and the formatting come from the server.
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
      `MLC: couldn't start "${command} lsp" (${err.message}). Adjust the mlc.path setting.`
    );
  });
  context.subscriptions.push({ dispose: () => client && client.stop() });
}

function deactivate() {
  return client ? client.stop() : undefined;
}

module.exports = { activate, deactivate };
