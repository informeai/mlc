-- Configuração do servidor para vim.lsp.config (Neovim 0.11+). Para usar outro
-- executável: vim.lsp.config("mlc", { cmd = { "/caminho/do/mlc", "lsp" } })
return {
  cmd = { "mlc", "lsp" },
  filetypes = { "mlc" },
  root_markers = { "main.mlc", ".git" },
}
