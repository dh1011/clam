# clam demo. Run from the repository root: vhs docs/demo.tape
Output docs/demo.gif

Set Shell bash
Set FontSize 18
Set Width 1200
Set Height 820
Set Padding 24
Set TypingSpeed 45ms
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful

# Setup off camera: a small sample project and a clean prompt.
Hide
Type "source docs/demo-setup.sh && clear"
Enter
Sleep 2s
Show

# Ordinary commands run as usual.
Type "ls"
Sleep 300ms
Enter
Sleep 1.2s

# Plain English gets an answer from the agent.
Type "what does this project do?"
Sleep 400ms
Enter
Wait@90s /weather-cli \$\s*$/
Sleep 3s

# Changes need approval.
Type "add a help option to the cli"
Sleep 400ms
Enter
Wait@90s /Allow this action/
Sleep 1.5s
Type "y"
Enter
Wait@90s /weather-cli \$\s*$/
Sleep 2s

Type "git diff --stat"
Enter
Sleep 3s

Type "node index.js --help"
Enter
Sleep 4s
