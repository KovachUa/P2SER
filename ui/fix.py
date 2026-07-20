with open("src/App.jsx", "r") as f:
    lines = f.readlines()

terminal_start = -1
terminal_end = -1
for i, line in enumerate(lines):
    if line.startswith("function TerminalWindow({ podId, podName, onClose }) {"):
        terminal_start = i
    if terminal_start != -1 and line == "}\n" and terminal_end == -1:
        # Check if the next line is empty or something
        if i - terminal_start > 50:
            terminal_end = i
            break

terminal_lines = lines[terminal_start:terminal_end+1]
del lines[terminal_start:terminal_end+1]

# Now we need to remove the "    </div>\n  )\n}\n" that was inserted before terminal_start
for i in range(terminal_start-1, -1, -1):
    if lines[i] == "}\n" and lines[i-1] == "  )\n" and lines[i-2] == "    </div>\n":
        del lines[i-2:i+1]
        break

# And put them back at the end before TerminalWindow
lines.append("    </div>\n")
lines.append("  )\n")
lines.append("}\n\n")
lines.extend(terminal_lines)

with open("src/App.jsx", "w") as f:
    f.writelines(lines)
