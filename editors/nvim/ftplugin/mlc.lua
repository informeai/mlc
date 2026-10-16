-- Indentação de 4 espaços, como o mlc fmt; comentários com //.
vim.bo.expandtab = true
vim.bo.shiftwidth = 4
vim.bo.tabstop = 4
vim.bo.softtabstop = 4
vim.bo.commentstring = "// %s"
vim.b.undo_ftplugin = "setlocal expandtab< shiftwidth< tabstop< softtabstop< commentstring<"
