package systems

import (
	"fmt"
	"strings"
)

// A COMMAND IS A LIST ON THE WIRE AND ONE LINE IN THE STEP. A call gives a list of arguments and
// never a shell line, and what runs is that list; the step's field (`secret.exec`'s `argv`) is
// the list spelled as one line, so a shape, a row and a person approving read a command line as
// they read `commands.run`'s, and anchor their patterns as they do there (`!regexp "^npm
// publish"`). The spelling is one function on both sides of the channel, and two lists never
// spell the same: an argument is written bare where every character of it is one a shell reads
// as itself (letters, digits and `_ - . / : = @ % + ,`), and in single quotes otherwise, a quote
// within it as `'\''`, so where one argument ends is never in doubt (`npm publish --tag 'a b'`).

// bare is the characters an argument may hold and be written as it is.
const bare = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:=@%+,"

// Argv is a command's arguments as one line (above).
func Argv(args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = quoteArg(a)
	}
	return strings.Join(words, " ")
}

func quoteArg(a string) string {
	if a != "" && strings.Trim(a, bare) == "" {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// ArgvList is the list a line of Argv spells, or why the line is no spelling of one: an
// argument bare that holds a character the spelling quotes, or a quote left open.
func ArgvList(line string) ([]string, error) {
	var out []string
	rs := []rune(line)
	for i := 0; i < len(rs); {
		if rs[i] == ' ' {
			i++
			continue
		}
		var b strings.Builder
		for i < len(rs) && rs[i] != ' ' {
			switch {
			case rs[i] == '\'':
				end := i + 1
				for end < len(rs) && rs[end] != '\'' {
					end++
				}
				if end == len(rs) {
					return nil, fmt.Errorf("a quote opened at %d is not closed", i)
				}
				b.WriteString(string(rs[i+1 : end]))
				i = end + 1
			case rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '\'':
				b.WriteByte('\'')
				i += 2
			case strings.ContainsRune(bare, rs[i]):
				b.WriteRune(rs[i])
				i++
			default:
				return nil, fmt.Errorf("%q at %d is written in quotes", rs[i], i)
			}
		}
		out = append(out, b.String())
	}
	return out, nil
}
