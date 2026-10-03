package delta

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// pathKey builds a canonical string identity for a decoded path. It preserves
// the distinction between a numeric index and a string object key.
func pathKey(path Path) string {
	var builder strings.Builder
	for index, segment := range path {
		if index > 0 {
			builder.WriteByte(',')
		}
		if key, ok := segment.(string); ok {
			builder.WriteByte('s')
			builder.WriteString(key)
			continue
		}
		if value, ok := toInt(segment); ok {
			builder.WriteByte('n')
			builder.WriteString(strconv.Itoa(value))
			continue
		}
		builder.WriteByte('?')
	}
	return builder.String()
}

// Encoder interns paths and omits repeats. One encoder belongs to exactly one
// independent state stream.
type Encoder struct {
	seen     map[string]bool
	ids      map[string]int
	nextID   int
	previous string
	hasPrev  bool
}

// NewEncoder returns a fresh encoder for one state stream.
func NewEncoder() *Encoder {
	return &Encoder{seen: map[string]bool{}, ids: map[string]int{}}
}

func (e *Encoder) reset() {
	e.seen = map[string]bool{}
	e.ids = map[string]int{}
	e.nextID = 0
	e.previous = ""
	e.hasPrev = false
}

// Encode compresses one decoded batch to its wire form. It does not mutate the
// input.
func (e *Encoder) Encode(ops []Op) ([]WireOp, error) {
	out := []WireOp{}
	e.previous = ""
	e.hasPrev = false
	for _, op := range ops {
		if err := validateOp(op); err != nil {
			return nil, err
		}
		verb := op[0].(string)
		if verb == "r" {
			out = append(out, WireOp{"r", op[1]})
			e.reset()
			continue
		}
		path, _ := asPath(op[1])
		key := pathKey(path)
		if e.hasPrev && key == e.previous {
			switch verb {
			case "s":
				out = append(out, WireOp{"s", op[2]})
			case "d":
				out = append(out, WireOp{"d"})
			case "a":
				out = append(out, WireOp{"a", op[2]})
			case "t":
				out = append(out, WireOp{"t", op[2]})
			case "p":
				out = append(out, WireOp{"p", op[2], op[3], op[4]})
			case "m":
				out = append(out, WireOp{"m", op[2]})
			}
			continue
		}
		var ref any = path
		if existing, ok := e.ids[key]; ok {
			ref = existing
		} else if e.seen[key] {
			id := e.nextID
			e.nextID++
			e.ids[key] = id
			out = append(out, WireOp{"#", id, path})
			ref = id
		} else {
			e.seen[key] = true
		}
		switch verb {
		case "s":
			out = append(out, WireOp{"s", ref, op[2]})
		case "d":
			out = append(out, WireOp{"d", ref})
		case "a":
			out = append(out, WireOp{"a", ref, op[2]})
		case "t":
			out = append(out, WireOp{"t", ref, op[2]})
		case "p":
			out = append(out, WireOp{"p", ref, op[2], op[3], op[4]})
		case "m":
			out = append(out, WireOp{"m", ref, op[2]})
		}
		e.previous = key
		e.hasPrev = true
	}
	return out, nil
}

// Decoder resolves interned paths and shortened tuples. One decoder belongs to
// exactly one independent state stream.
type Decoder struct {
	paths map[int]Path
}

// NewDecoder returns a fresh decoder for one state stream.
func NewDecoder() *Decoder {
	return &Decoder{paths: map[int]Path{}}
}

func okRef(value any) error {
	if _, ok := toInt(value); ok {
		if index, _ := toInt(value); index < 0 {
			return errors.New("delta: bad path id")
		}
		return nil
	}
	if _, ok := asPath(value); !ok {
		return errBadPath
	}
	return validatePathArg(value, false)
}

func validateWireOp(op WireOp) error {
	if len(op) == 0 {
		return errNotTuple
	}
	verb, ok := op[0].(string)
	if !ok {
		return errNotTuple
	}
	switch verb {
	case "r":
		if len(op) != 2 {
			return fmt.Errorf("delta: invalid wire arity for r")
		}
		if !IsJSONValue(op[1]) {
			return fmt.Errorf("%w: replacement", errNotJSON)
		}
	case "s":
		switch len(op) {
		case 3:
			if err := okRef(op[1]); err != nil {
				return err
			}
		case 2:
		default:
			return errors.New("delta: invalid wire arity for s")
		}
	case "d":
		switch len(op) {
		case 2:
			if err := okRef(op[1]); err != nil {
				return err
			}
		case 1:
		default:
			return errors.New("delta: invalid wire arity for d")
		}
	case "a":
		switch len(op) {
		case 3:
			if err := okRef(op[1]); err != nil {
				return err
			}
			if _, ok := op[2].(string); !ok {
				return errors.New("delta: a value is not a string")
			}
		case 2:
			if _, ok := op[1].(string); !ok {
				return errors.New("delta: a value is not a string")
			}
		default:
			return errors.New("delta: invalid wire arity for a")
		}
	case "t":
		switch len(op) {
		case 3:
			if err := okRef(op[1]); err != nil {
				return err
			}
			if count, ok := toInt(op[2]); !ok || count < 0 {
				return errors.New("delta: t count is not a non-negative integer")
			}
		case 2:
			if count, ok := toInt(op[1]); !ok || count < 0 {
				return errors.New("delta: t count is not a non-negative integer")
			}
		default:
			return errors.New("delta: invalid wire arity for t")
		}
	case "p":
		var index, remove, items any
		switch len(op) {
		case 5:
			if err := okRef(op[1]); err != nil {
				return err
			}
			index, remove, items = op[2], op[3], op[4]
		case 4:
			index, remove, items = op[1], op[2], op[3]
		default:
			return errors.New("delta: invalid wire arity for p")
		}
		if value, ok := toInt(index); !ok || value < 0 {
			return errors.New("delta: p index is not a non-negative integer")
		}
		if value, ok := toInt(remove); !ok || value < 0 {
			return errors.New("delta: p remove is not a non-negative integer")
		}
		if _, ok := asSlice(items); !ok {
			return errors.New("delta: p items is not an array")
		}
	case "m":
		var permutation any
		switch len(op) {
		case 3:
			if err := okRef(op[1]); err != nil {
				return err
			}
			permutation = op[2]
		case 2:
			permutation = op[1]
		default:
			return errors.New("delta: invalid wire arity for m")
		}
		if err := validatePermutation(permutation); err != nil {
			return err
		}
	case "#":
		if len(op) != 3 {
			return errors.New("delta: invalid wire arity for #")
		}
		if id, ok := toInt(op[1]); !ok || id < 0 {
			return errors.New("delta: bad path id")
		}
		if _, ok := asPath(op[2]); !ok {
			return errBadPath
		}
		if err := validatePathArg(op[2], false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("delta: unknown wire verb: %v", op[0])
	}
	return nil
}

// Decode expands one wire batch back into decoded operations. It does not
// mutate the input.
func (d *Decoder) Decode(wire []WireOp) ([]Op, error) {
	var previous Path
	hasPrev := false
	out := []Op{}
	for _, op := range wire {
		if err := validateWireOp(op); err != nil {
			return nil, err
		}
		verb := op[0].(string)
		if verb == "#" {
			id, _ := toInt(op[1])
			path, _ := asPath(op[2])
			d.paths[id] = Path(append([]any(nil), path...))
			continue
		}
		if verb == "r" {
			out = append(out, Op{"r", op[1]})
			d.paths = map[int]Path{}
			previous = nil
			hasPrev = false
			continue
		}
		short := false
		switch verb {
		case "d":
			short = len(op) == 1
		case "s", "a", "t", "m":
			short = len(op) == 2
		case "p":
			short = len(op) == 4
		}
		var path Path
		if short {
			if !hasPrev {
				return nil, errors.New("delta: wire batch omits the first path")
			}
			path = previous
		} else {
			ref := op[1]
			if id, ok := toInt(ref); ok {
				resolved, present := d.paths[id]
				if !present {
					return nil, fmt.Errorf("delta: unknown path id %d", id)
				}
				path = resolved
			} else {
				path, _ = asPath(ref)
			}
			previous = path
			hasPrev = true
		}
		if verb != "p" && verb != "m" && len(path) == 0 {
			return nil, errEmptyPath
		}
		switch verb {
		case "s":
			if short {
				out = append(out, Op{"s", path, op[1]})
			} else {
				out = append(out, Op{"s", path, op[2]})
			}
		case "d":
			out = append(out, Op{"d", path})
		case "a":
			if short {
				out = append(out, Op{"a", path, op[1]})
			} else {
				out = append(out, Op{"a", path, op[2]})
			}
		case "t":
			if short {
				out = append(out, Op{"t", path, op[1]})
			} else {
				out = append(out, Op{"t", path, op[2]})
			}
		case "p":
			if short {
				out = append(out, Op{"p", path, op[1], op[2], op[3]})
			} else {
				out = append(out, Op{"p", path, op[2], op[3], op[4]})
			}
		case "m":
			if short {
				out = append(out, Op{"m", path, op[1]})
			} else {
				out = append(out, Op{"m", path, op[2]})
			}
		}
	}
	return out, nil
}
