-- Hooks mlc lsp up to .mlc files: errors as you type (vim.diagnostic), semantic
-- colors and formatting (vim.lsp.buf.format). The configuration is in
-- lsp/mlc.lua.

-- also in ftdetect/; repeated here for when the plugin enters the
-- runtimepath after file type detection was already turned on
vim.filetype.add({ extension = { mlc = "mlc" } })

if vim.fn.has("nvim-0.11") == 1 then
  vim.lsp.enable("mlc")
end
