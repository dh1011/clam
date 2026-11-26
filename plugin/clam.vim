" clam: answer English sentences typed after :!, like at the prompt.
" The clam repo is a Vim/Neovim plugin; this points 'shell' at `clam shell`,
" which runs anything that isn't English in $SHELL as usual. Needs the clam
" binary on PATH.
"
" Vim:     ln -s /path/to/clam ~/.vim/pack/clam/start/clam
" Neovim:  lazy.nvim spec { dir = "/path/to/clam" }

if exists('g:loaded_clam') || has('win32') || !executable('clam')
  finish
endif
let g:loaded_clam = 1
let $CLAM_SESSION_ID = 'vim-' . getpid()
let &shell = 'clam shell'
