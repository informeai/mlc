-- Liga o mlc lsp aos arquivos .mlc: erros ao digitar (vim.diagnostic), cores
-- semânticas e formatação (vim.lsp.buf.format). A configuração está em
-- lsp/mlc.lua.

-- também em ftdetect/; repetido aqui para quando o plugin entra no
-- runtimepath depois de a detecção de tipos já ter sido ligada
vim.filetype.add({ extension = { mlc = "mlc" } })

if vim.fn.has("nvim-0.11") == 1 then
  vim.lsp.enable("mlc")
end
