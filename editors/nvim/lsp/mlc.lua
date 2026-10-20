-- Server configuration for vim.lsp.config (Neovim 0.11+). To use another
-- executable: vim.lsp.config("mlc", { cmd = { "/path/to/mlc", "lsp" } })
return {
  cmd = { "mlc", "lsp" },
  filetypes = { "mlc" },
  root_markers = { "main.mlc", ".git" },
}
