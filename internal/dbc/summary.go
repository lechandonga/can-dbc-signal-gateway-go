package dbc

import "sort"

// ImportSummary 描述一份 DBC 导入时的采用、忽略与拒绝明细。
type ImportSummary struct {
	AdoptedMessages []MessageSummary
	Ignored         []IgnoredItem
	Rejected        []*DefError
}

// MessageSummary 为被采用报文及其信号摘要。
type MessageSummary struct {
	ID          uint32
	Name        string
	DLC         int
	SignalNames []string
}

// IgnoredItem 是明确识别但不影响解码的内容。
type IgnoredItem struct {
	Kind      string
	FirstLine int
	LastLine  int
	Count     int
	Reason    string
}

func (p *parser) summary() *ImportSummary {
	sum := &ImportSummary{Rejected: []*DefError{}}
	ids := make([]uint32, 0, len(p.msgs))
	for id := range p.msgs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		msg := p.msgs[id]
		names := make([]string, 0, len(msg.Signals))
		for _, sig := range msg.Signals {
			names = append(names, sig.Name)
		}
		sum.AdoptedMessages = append(sum.AdoptedMessages, MessageSummary{
			ID: id, Name: msg.Name, DLC: msg.DLC, SignalNames: names,
		})
	}
	kinds := make([]string, 0, len(p.ignored))
	for kind := range p.ignored {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		g := p.ignored[kind]
		sum.Ignored = append(sum.Ignored, IgnoredItem{
			Kind: kind, FirstLine: g.firstLine, LastLine: g.lastLine, Count: g.count,
			Reason: ignoredReason(kind),
		})
	}
	return sum
}

func ignoredReason(kind string) string {
	switch kind {
	case "BA_", "BA_DEF_", "BA_DEF_DEF_":
		return "attribute metadata does not change bit decoding"
	case "SIG_GROUP_":
		return "signal grouping does not change bit decoding"
	case "BO_TX_BU_":
		return "transmitter list does not change bit decoding"
	default:
		return "recognized metadata does not change bit decoding"
	}
}
