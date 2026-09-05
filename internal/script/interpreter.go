// Package script implements a small, embeddable templating language for
// defining chat-bot command responses.
//
// The syntax deliberately mirrors the "$(...)" variable/function
// convention used by StreamElements and Nightbot custom commands
// ($(user), $(touser), $(count), $(urlfetch ...), etc).
// This allows for command definitions written here are already close
// to what streamers and mods commonly use elsewhere, making for easy porting.
//
// Beyond the basic StreamElements-style variable set, this package adds
// a handful of primitives needed for command logic with minimal hardcoding:
//
//   - $(let name|value|body)  -- bind a value once, reuse it via $(var name)
//   - $(try a|b|c...)          -- evaluate in order, return the first success
//   - $(catch expr|errvar|handler) -- run handler (with the error message
//     bound to errvar) only if expr fails
//   - $(fail message)          -- deliberately raise an error
//   - $(if cond|then|else), $(eq a|b), $(gt a|b), $(lt a|b), $(gte a|b), $(lte a|b)
//   - $(regexfind pattern|text), $(slice text|start|end)
//   - $(plural count|word)
//   - $(config key), $(hasconfig key), $(requireconfig message|key1|key2...)
//   - $(available name) -- whether a given capability is wired up in Env
//
// This package knows nothing about Twitch, chat libraries, or any
// particular bot's config format. Callers adapt their own types into an
// Env, which keeps the interpreter reusable and unit-testable on its own.
package script

import (
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Env supplies everything the interpreter needs from the outside world.
// Any field left nil simply makes the corresponding builtin(s) report an
// error (or, via $(available), a checkable false) if a script tries to
// use them, so a caller only needs to wire up what it wants scripts to
// be able to do.
type Env struct {
	ChannelName string
	Nickname    string

	// Config holds arbitrary key/value lookups exposed to scripts via
	// $(config <key>) -- e.g. social links, external-API usernames.
	Config map[string]string

	// UrlFetch returns the contents of a given URL.
	UrlFetch func(url string) (string, error)

	// GetFollowers returns the channel's current follower count.
	GetFollowers func() (int, error)

	// GetViewers returns the channel's current viewer count.
	GetViewers func() (int, error)

	// GetUptime returns the stream's current uptime and whether it's
	// live at all.
	GetUptime func() (d time.Duration, live bool, err error)

	// CreateClip creates a new clip and returns its ID.
	CreateClip func() (string, error)

	// ResolveUser looks a login up and returns a stable user ID.
	// Returns an error if the user can't be found (or the lookup
	// otherwise fails).
	ResolveUser func(login string) (id string, err error)

	// GetFollowage returns how long the user with the given ID has
	// followed the channel, and whether they follow at all.
	GetFollowage func(userID string) (d time.Duration, following bool, err error)

	// Counter increments a named persistent counter by delta and
	// returns its new value. Pass delta 0 to read without mutating.
	Counter func(name string, delta int) (int, error)

	// FormatDuration renders a duration for chat. Optional -- a plain
	// default is used if nil.
	FormatDuration func(time.Duration) string

	// Pluralize returns a pluralized version of word if count is not 1.
	Pluralize func(count int, word string) string
}

// Context is the per-invocation state passed to Evaluate.
type Context struct {
	Username    string   // The sender's Twitch username
	UserID      string   // The sender's Twitch user ID
	Args        []string // The command arguments
	IsMod       bool     // Whether the sender is a moderator
	CommandName string   // The command name
	Env         *Env     // The interpreter environment

	rng  *rand.Rand        // A unified random number generator
	vars map[string]string // Variables bound with $(let)
}

// rand returns a unified random number generator.
func (c *Context) rand() *rand.Rand {
	if c.rng == nil {
		c.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return c.rng
}

// getVar returns the value of name, if it has one.
func (c *Context) getVar(name string) (string, bool) {
	if c.vars == nil {
		return "", false
	}
	v, ok := c.vars[name]
	return v, ok
}

// setVar sets name to value for the remainder of this Context's
// lifetime, returning a restore func that puts back whatever was there
// before (or removes it, if it didn't exist), so scoping doesn't
// leak across sibling branches.
func (c *Context) setVar(name, value string) (restore func()) {
	if c.vars == nil {
		c.vars = map[string]string{}
	}
	old, had := c.vars[name]
	c.vars[name] = value
	return func() {
		if had {
			c.vars[name] = old
		} else {
			delete(c.vars, name)
		}
	}
}

// Evaluate resolves every $(...) expression in tmpl against ctx and
// returns the resulting text.
func Evaluate(tmpl string, ctx *Context) (string, error) {
	var out strings.Builder
	i := 0
	for i < len(tmpl) {
		start := strings.Index(tmpl[i:], "$(")
		if start == -1 {
			out.WriteString(tmpl[i:])
			break
		}
		start += i
		out.WriteString(tmpl[i:start])

		end, err := matchingParen(tmpl, start+2)
		if err != nil {
			return "", err
		}
		inner := tmpl[start+2 : end]
		resolved, err := evalExpr(inner, ctx)
		if err != nil {
			return "", err
		}
		out.WriteString(resolved)
		i = end + 1
	}
	return out.String(), nil
}

// matchingParen returns the index of the ')' that closes the "$(" whose
// contents start at openIdx. Depth is tracked on *every* '(' and ')' --
// not just ones belonging to "$(" -- so balanced literal parentheses in
// argument text (regex groups, emoticons like "(´꒳`)♡(´꒳`)") pass
// through untouched instead of prematurely closing the expression.
// There is deliberately no backslash-escape mechanism here: regex
// patterns routinely contain literal backslashes (\d, \s, \w...), and
// an escape character would either have to special-case those away or
// silently eat them -- unbalanced literal parens are rare enough in
// practice that requiring balance is simpler for now.
func matchingParen(s string, openIdx int) (int, error) {
	depth := 1
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unclosed %q in template", "$(")
}

// splitTopLevel splits s on sep, but only at paren-depth 0, so a
// separator inside a nested $(...) or inside balanced literal parens
// (e.g. a regex alternation group) is not treated as a split point. See
// matchingParen for why there's no backslash-escape here.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(':
			depth++
			cur.WriteByte(c)
		case c == ')':
			depth--
			cur.WriteByte(c)
		case c == sep && depth == 0:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// splitNameArg splits raw into a function/variable name and the
// (unevaluated) remainder, on the first top-level space.
func splitNameArg(raw string) (name, arg string) {
	depth := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ' ':
			if depth == 0 {
				return raw[:i], raw[i+1:]
			}
		}
	}
	return raw, ""
}

// evalExpr evaluates the contents of a single $(...) group: it splits
// out the function/variable name and dispatches to the matching
// builtin, handing it the *raw, unevaluated* argument text. Builtins
// that need to inspect a sub-part before deciding whether to evaluate
// it at all (if/try/catch/let) rely on this laziness for correct
// short-circuiting -- e.g. $(if) must not evaluate its "then" branch
// when the condition is false, or a side-effecting call in that branch
// (a counter increment, an API call) would fire regardless of the
// branch taken.
func evalExpr(raw string, ctx *Context) (string, error) {
	name, arg := splitNameArg(strings.TrimSpace(raw))
	name = strings.ToLower(name)

	if n, err := strconv.Atoi(name); err == nil {
		return positionalArg(ctx, n), nil
	}

	fn, ok := builtins[name]
	if !ok {
		return "", fmt.Errorf("unknown variable or function %q", name)
	}
	return fn(ctx, arg)
}

// positionalArg returns the nth positional argument, or an empty string
func positionalArg(ctx *Context, n int) string {
	if n < 1 || n > len(ctx.Args) {
		return ""
	}
	return ctx.Args[n-1]
}

// BuiltinFunc is the signature for a builtin variable/function usable
// from templates. arg is the RAW, unevaluated text after the name --
// most builtins should call Evaluate(arg, ctx) themselves; control-flow
// builtins may instead use splitTopLevel and evaluate only the parts
// they need.
type BuiltinFunc func(ctx *Context, arg string) (string, error)

// RegisterBuiltin adds or overrides a builtin available to every
// template. Call it during program start-up to extend the language with
// bot- or game-specific functions without touching this package. Names
// are case-insensitive.
func RegisterBuiltin(name string, fn BuiltinFunc) {
	builtins[strings.ToLower(name)] = fn
}

// eval1 evaluates raw as a single sub-expression, trimming whitespace.
func eval1(ctx *Context, raw string) (string, error) {
	v, err := Evaluate(raw, ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// builtins is populated in init() rather than via a direct map literal
// initializer: several entries call eval1/Evaluate, which themselves
// dispatch back through builtins, and Go's initialization-cycle checker
// flags that self-reference even though it's only ever exercised at
// call time, long after builtins is fully populated.
var builtins map[string]BuiltinFunc

// init populates builtins
func init() {
	builtins = map[string]BuiltinFunc{
		"user":   func(ctx *Context, arg string) (string, error) { return ctx.Username, nil },
		"sender": func(ctx *Context, arg string) (string, error) { return ctx.Username, nil },
		"args":   func(ctx *Context, arg string) (string, error) { return strings.Join(ctx.Args, " "), nil },

		"channel":  func(ctx *Context, arg string) (string, error) { return ctx.Env.ChannelName, nil },
		"streamer": func(ctx *Context, arg string) (string, error) { return ctx.Env.Nickname, nil },

		"ismod": func(ctx *Context, arg string) (string, error) {
			return strconv.FormatBool(ctx.IsMod), nil
		},

		"touser": func(ctx *Context, arg string) (string, error) {
			if len(ctx.Args) > 0 {
				return strings.TrimPrefix(strings.ToLower(ctx.Args[0]), "@"), nil
			}
			return ctx.Env.ChannelName, nil
		},

		"rand": func(ctx *Context, arg string) (string, error) {
			options := splitTopLevel(arg, '|')
			if len(options) == 0 || (len(options) == 1 && strings.TrimSpace(options[0]) == "") {
				return "", fmt.Errorf("$(rand) needs at least one option")
			}
			return eval1(ctx, options[ctx.rand().Intn(len(options))])
		},

		"randnum": func(ctx *Context, arg string) (string, error) {
			resolved, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			lo, hi, ok := parseRange(resolved)
			if !ok {
				return "", fmt.Errorf("$(randnum) needs \"min max\", got %q", resolved)
			}
			return strconv.Itoa(lo + ctx.rand().Intn(hi-lo+1)), nil
		},

		// if/try/catch/let are the control-flow primitives: they receive
		// the raw, unsplit argument and decide for themselves what to
		// evaluate, so unused branches never run (and never side-effect).
		"if": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) != 3 {
				return "", fmt.Errorf("$(if) needs cond|then|else, got %d part(s)", len(parts))
			}
			cond, err := eval1(ctx, parts[0])
			if err != nil {
				return "", err
			}
			if cond != "" && cond != "false" && cond != "0" {
				return Evaluate(parts[1], ctx)
			}
			return Evaluate(parts[2], ctx)
		},

		// try evaluates each pipe-separated option in order and returns the
		// first one that succeeds. If every option fails, it returns the
		// last error. This is the template-level equivalent of the old
		// Go pattern "try the primary API, fall back to the backup", and
		// also covers "call this API, or say something friendly if it's
		// unavailable".
		"try": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) < 2 {
				return "", fmt.Errorf("$(try) needs at least two options")
			}
			var lastErr error
			for _, p := range parts {
				res, err := Evaluate(p, ctx)
				if err == nil {
					return res, nil
				}
				lastErr = err
			}
			return "", lastErr
		},

		// catch is try's more powerful sibling: it runs a handler only on
		// failure, with the error's message bound to a variable so the
		// handler can inspect, log, or re-raise it under a different
		// message via $(fail).
		"catch": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) != 3 {
				return "", fmt.Errorf("$(catch) needs expr|errvar|handler, got %d part(s)", len(parts))
			}
			res, err := Evaluate(parts[0], ctx)
			if err == nil {
				return res, nil
			}
			varName, verr := eval1(ctx, parts[1])
			if verr != nil {
				return "", verr
			}
			restore := ctx.setVar(varName, err.Error())
			defer restore()
			return Evaluate(parts[2], ctx)
		},

		// fail always errors with the given (evaluated) message. Used to
		// re-raise a specific, exact error message from within a $(catch)
		// handler, or to hard-stop a template deliberately.
		"fail": func(ctx *Context, arg string) (string, error) {
			msg, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("%s", msg)
		},

		// let evaluates value once, makes it available as $(var name) while
		// body is evaluated, and restores any previous binding of name
		// afterward. Exists so a value that's expensive or side-effecting to
		// produce (an HTTP fetch, a counter increment) is computed exactly
		// once even if the template needs it in more than one place.
		"let": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) != 3 {
				return "", fmt.Errorf("$(let) needs name|value|body, got %d part(s)", len(parts))
			}
			name, err := eval1(ctx, parts[0])
			if err != nil {
				return "", err
			}
			val, err := Evaluate(parts[1], ctx)
			if err != nil {
				return "", err
			}
			restore := ctx.setVar(name, val)
			defer restore()
			return Evaluate(parts[2], ctx)
		},

		"var": func(ctx *Context, arg string) (string, error) {
			name, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			v, ok := ctx.getVar(name)
			if !ok {
				return "", fmt.Errorf("no such variable %q (use $(let) to define it)", name)
			}
			return v, nil
		},

		"eq": func(ctx *Context, arg string) (string, error) {
			return compareStrings(ctx, arg, func(a, b string) bool { return a == b })
		},
		"neq": func(ctx *Context, arg string) (string, error) {
			return compareStrings(ctx, arg, func(a, b string) bool { return a != b })
		},
		"gt": func(ctx *Context, arg string) (string, error) {
			return compareNumbers(ctx, arg, func(a, b float64) bool { return a > b })
		},
		"lt": func(ctx *Context, arg string) (string, error) {
			return compareNumbers(ctx, arg, func(a, b float64) bool { return a < b })
		},
		"gte": func(ctx *Context, arg string) (string, error) {
			return compareNumbers(ctx, arg, func(a, b float64) bool { return a >= b })
		},
		"lte": func(ctx *Context, arg string) (string, error) {
			return compareNumbers(ctx, arg, func(a, b float64) bool { return a <= b })
		},

		// plural uses the pluralize() helper directly:
		// the word gets an "s" appended unless count == 1.
		"plural": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) != 2 {
				return "", fmt.Errorf("$(plural) needs count|word, got %d part(s)", len(parts))
			}
			countStr, err := eval1(ctx, parts[0])
			if err != nil {
				return "", err
			}
			word, err := eval1(ctx, parts[1])
			if err != nil {
				return "", err
			}
			n, err := strconv.Atoi(countStr)
			if err != nil {
				return "", fmt.Errorf("$(plural) needs a numeric count, got %q", countStr)
			}

			return ctx.Env.Pluralize(n, word), nil
		},

		// regexfind returns the first match of pattern in text (Go RE2
		// syntax), or errors if there's no match -- pair it with $(try) or
		// $(catch) to supply a fallback.
		"regexfind": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) != 2 {
				return "", fmt.Errorf("$(regexfind) needs pattern|text, got %d part(s)", len(parts))
			}
			pattern, err := eval1(ctx, parts[0])
			if err != nil {
				return "", err
			}
			text, err := Evaluate(parts[1], ctx)
			if err != nil {
				return "", err
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", fmt.Errorf("bad regex %q: %w", pattern, err)
			}
			m := re.FindString(text)
			if m == "" {
				return "", fmt.Errorf("no match for pattern %q", pattern)
			}
			return m, nil
		},

		// slice returns text[start:end], rune-indexed, Python-style (a
		// negative index counts from the end).
		"slice": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) != 3 {
				return "", fmt.Errorf("$(slice) needs text|start|end, got %d part(s)", len(parts))
			}
			text, err := Evaluate(parts[0], ctx)
			if err != nil {
				return "", err
			}
			startStr, err := eval1(ctx, parts[1])
			if err != nil {
				return "", err
			}
			endStr, err := eval1(ctx, parts[2])
			if err != nil {
				return "", err
			}
			start, err := strconv.Atoi(startStr)
			if err != nil {
				return "", fmt.Errorf("$(slice) needs a numeric start, got %q", startStr)
			}
			end, err := strconv.Atoi(endStr)
			if err != nil {
				return "", fmt.Errorf("$(slice) needs a numeric end, got %q", endStr)
			}
			runes := []rune(text)
			start = normalizeIndex(start, len(runes))
			end = normalizeIndex(end, len(runes))
			if start < 0 {
				start = 0
			}
			if end > len(runes) {
				end = len(runes)
			}
			if start >= end {
				return "", nil
			}
			return string(runes[start:end]), nil
		},

		"config": func(ctx *Context, arg string) (string, error) {
			key, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			v, ok := ctx.Env.Config[key]
			if !ok || v == "" {
				return "", fmt.Errorf("config value %q is not set", key)
			}
			return v, nil
		},

		"hasconfig": func(ctx *Context, arg string) (string, error) {
			key, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			v, ok := ctx.Env.Config[key]
			return strconv.FormatBool(ok && v != ""), nil
		},

		// requireconfig checks that every listed key is set, and fails with
		// EXACTLY the given message if any of them is missing or empty --
		// e.g. $(requireconfig config missing discord link|discord),
		// giving per-command validation error text instead of a generic
		// error. Expands to nothing on success.
		"requireconfig": func(ctx *Context, arg string) (string, error) {
			parts := splitTopLevel(arg, '|')
			if len(parts) < 2 {
				return "", fmt.Errorf("$(requireconfig) needs message|key1|key2..., got %d part(s)", len(parts))
			}
			msg, err := eval1(ctx, parts[0])
			if err != nil {
				return "", err
			}
			for _, p := range parts[1:] {
				key, err := eval1(ctx, p)
				if err != nil {
					return "", err
				}
				v, ok := ctx.Env.Config[key]
				if !ok || v == "" {
					return "", fmt.Errorf("%s", msg)
				}
			}
			return "", nil
		},

		// available reports whether a given capability is wired up in Env,
		// letting a template branch on "h.apiClient == nil || h.broadcaster == nil"
		//  -- e.g. $(if $(available clip)|$(clip)|API client not configured.)
		"available": func(ctx *Context, arg string) (string, error) {
			name, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			var ok bool
			switch strings.ToLower(name) {
			case "urlfetch":
				ok = ctx.Env.UrlFetch != nil
			case "followers":
				ok = ctx.Env.GetFollowers != nil
			case "viewers":
				ok = ctx.Env.GetViewers != nil
			case "uptime":
				ok = ctx.Env.GetUptime != nil
			case "clip":
				ok = ctx.Env.CreateClip != nil
			case "followage":
				ok = ctx.Env.ResolveUser != nil && ctx.Env.GetFollowage != nil
			case "counter":
				ok = ctx.Env.Counter != nil
			default:
				return "", fmt.Errorf("$(available) doesn't recognize %q", name)
			}
			return strconv.FormatBool(ok), nil
		},

		// count and counter increment counters for the current command
		"count": func(ctx *Context, arg string) (string, error) {
			return countHelper(ctx, ctx.CommandName, 1)
		},
		"counter": func(ctx *Context, arg string) (string, error) {
			name, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			return countHelper(ctx, name, 1)
		},
		// countof returns the current value of a counter
		"countof": func(ctx *Context, arg string) (string, error) {
			name, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			if name == "" {
				name = ctx.CommandName
			}
			return countHelper(ctx, name, 0)
		},

		// urlfetch fetches data from a URL
		"urlfetch": func(ctx *Context, arg string) (string, error) {
			if ctx.Env.UrlFetch == nil {
				return "", fmt.Errorf("urlfetch is not available")
			}
			url, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			return ctx.Env.UrlFetch(url)
		},

		// followers returns the number of streamer followers
		"followers": func(ctx *Context, arg string) (string, error) {
			if ctx.Env.GetFollowers == nil {
				return "", fmt.Errorf("follower lookup is not available")
			}
			n, err := ctx.Env.GetFollowers()
			if err != nil {
				return "", err
			}
			return strconv.Itoa(n), nil
		},

		// viewers returns the number of stream viewers
		"viewers": func(ctx *Context, arg string) (string, error) {
			if ctx.Env.GetViewers == nil {
				return "", fmt.Errorf("viewer lookup is not available")
			}
			n, err := ctx.Env.GetViewers()
			if err != nil {
				return "", err
			}
			return strconv.Itoa(n), nil
		},

		// uptime returns the streamer's uptime
		"uptime": func(ctx *Context, arg string) (string, error) {
			if ctx.Env.GetUptime == nil {
				return "", fmt.Errorf("uptime lookup is not available")
			}
			d, live, err := ctx.Env.GetUptime()
			if err != nil {
				return "", err
			}
			if !live {
				return "offline", nil
			}
			return formatDuration(ctx, d), nil
		},

		// islive is split out from uptime so templates can branch on it with
		// $(if) instead of string-comparing against "offline".
		"islive": func(ctx *Context, arg string) (string, error) {
			if ctx.Env.GetUptime == nil {
				return "", fmt.Errorf("uptime lookup is not available")
			}
			_, live, err := ctx.Env.GetUptime()
			if err != nil {
				return "", err
			}
			return strconv.FormatBool(live), nil
		},

		// clip creates a new clip and returns its URL
		"clip": func(ctx *Context, arg string) (string, error) {
			if ctx.Env.CreateClip == nil {
				return "", fmt.Errorf("clip creation is not available")
			}
			return ctx.Env.CreateClip()
		},

		// resolveuser returns a stable ID for a login (or, with no argument,
		// the invoking user's own ID -- no lookup needed, same shortcut the
		// original Go code took for "check my own followage").
		"resolveuser": func(ctx *Context, arg string) (string, error) {
			login, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			if login == "" {
				return ctx.UserID, nil
			}
			if ctx.Env.ResolveUser == nil {
				return "", fmt.Errorf("user lookup is not available")
			}
			return ctx.Env.ResolveUser(login)
		},

		// followedfor returns "not following" (a normal, successful result,
		// not an error) if the given user ID doesn't follow, or a formatted
		// duration if they do. It only errors on a genuine lookup failure.
		"followedfor": func(ctx *Context, arg string) (string, error) {
			id, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			if ctx.Env.GetFollowage == nil {
				return "", fmt.Errorf("followage lookup is not available")
			}
			d, following, err := ctx.Env.GetFollowage(id)
			if err != nil {
				return "", err
			}
			if !following {
				return "not following", nil
			}
			return formatDuration(ctx, d), nil
		},

		// followage is convenience sugar for the common case (no need to
		// distinguish "user not found" from other failures): empty argument
		// means self.
		"followage": func(ctx *Context, arg string) (string, error) {
			login, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			var id string
			if login == "" {
				id = ctx.UserID
			} else {
				if ctx.Env.ResolveUser == nil {
					return "", fmt.Errorf("user lookup is not available")
				}
				id, err = ctx.Env.ResolveUser(login)
				if err != nil {
					return "", err
				}
			}
			return builtins["followedfor"](ctx, id)
		},

		// formatduration exposes the exact same duration formatting uptime
		// and followage use, for templates that compute their own seconds
		// value instead of going through those builtins.
		"formatduration": func(ctx *Context, arg string) (string, error) {
			s, err := eval1(ctx, arg)
			if err != nil {
				return "", err
			}
			secs, err := strconv.Atoi(s)
			if err != nil {
				return "", fmt.Errorf("$(formatduration) needs a number of seconds, got %q", s)
			}
			return formatDuration(ctx, time.Duration(secs)*time.Second), nil
		},
	}
}

// compareStrings evaluates whether two strings return true when compared via cmp.
func compareStrings(ctx *Context, arg string, cmp func(a, b string) bool) (string, error) {
	parts := splitTopLevel(arg, '|')
	if len(parts) != 2 {
		return "", fmt.Errorf("comparison needs a|b, got %d part(s)", len(parts))
	}
	a, err := eval1(ctx, parts[0])
	if err != nil {
		return "", err
	}
	b, err := eval1(ctx, parts[1])
	if err != nil {
		return "", err
	}
	return strconv.FormatBool(cmp(a, b)), nil
}

// compareNumbers evaluates whether two numbers return true when compared via cmp.
func compareNumbers(ctx *Context, arg string, cmp func(a, b float64) bool) (string, error) {
	parts := splitTopLevel(arg, '|')
	if len(parts) != 2 {
		return "", fmt.Errorf("comparison needs a|b, got %d part(s)", len(parts))
	}
	aStr, err := eval1(ctx, parts[0])
	if err != nil {
		return "", err
	}
	bStr, err := eval1(ctx, parts[1])
	if err != nil {
		return "", err
	}
	a, err := strconv.ParseFloat(aStr, 64)
	if err != nil {
		return "", fmt.Errorf("comparison needs a number, got %q", aStr)
	}
	b, err := strconv.ParseFloat(bStr, 64)
	if err != nil {
		return "", fmt.Errorf("comparison needs a number, got %q", bStr)
	}
	return strconv.FormatBool(cmp(a, b)), nil
}

// countHelper is a helper for the count builtin, minimising duplication.
func countHelper(ctx *Context, name string, delta int) (string, error) {
	if ctx.Env.Counter == nil {
		return "", fmt.Errorf("counters are not available")
	}
	if name == "" {
		return "", fmt.Errorf("counter needs a name")
	}
	n, err := ctx.Env.Counter(name, delta)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(n), nil
}

// formatDuration is a helper for the formatduration builtin, calls the
// environment's FormatDuration function if available, otherwise uses a
// simple default, "d.Round(time.Second).String()".
func formatDuration(ctx *Context, d time.Duration) string {
	if ctx.Env.FormatDuration != nil {
		return ctx.Env.FormatDuration(d)
	}
	return d.Round(time.Second).String()
}

// parseRange is a helper for the random number builtin.
func parseRange(arg string) (lo, hi int, ok bool) {
	fields := strings.Fields(arg)
	if len(fields) != 2 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(fields[0])
	hi, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// normalizeIndex normalises negative indices for simplicity.
func normalizeIndex(idx, length int) int {
	if idx < 0 {
		return length + idx
	}
	return idx
}
