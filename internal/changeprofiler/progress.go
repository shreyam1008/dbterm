package changeprofiler

import "math"

type progressMeter struct {
	plans       []TablePlan
	indexByName map[string]int
	prefix      []int64
	total       int64
	allKnown    bool
	exact       bool
}

func newProgressMeter(plans []TablePlan, exact bool) *progressMeter {
	meter := &progressMeter{
		plans:       append([]TablePlan(nil), plans...),
		indexByName: make(map[string]int, len(plans)),
		prefix:      make([]int64, len(plans)+1),
		allKnown:    len(plans) > 0,
		exact:       exact,
	}
	for index, plan := range plans {
		meter.indexByName[plan.Name] = index
		if plan.EstimatedRows <= 0 {
			meter.allKnown = false
		}
		meter.prefix[index+1] = meter.prefix[index] + maxInt64(plan.EstimatedRows, 0)
	}
	meter.total = meter.prefix[len(meter.prefix)-1]
	return meter
}

func (m *progressMeter) decorate(event Progress, done bool) Progress {
	if m == nil || len(m.plans) == 0 {
		return event
	}
	index, ok := m.indexByName[event.Table]
	if !ok {
		return event
	}
	event.TableIndex = index + 1
	event.TableCount = len(m.plans)
	event.EstimatedRows = m.plans[index].EstimatedRows
	event.Approximate = !m.exact
	if m.allKnown && m.total > 0 {
		current := minInt64(event.Rows, event.EstimatedRows)
		if done {
			current = event.EstimatedRows
		}
		event.Percent = boundedPercent(float64(m.prefix[index]+current) / float64(m.total))
		return event
	}
	fraction := 0.0
	if done {
		fraction = 1
	} else if event.EstimatedRows > 0 {
		fraction = math.Min(float64(event.Rows)/float64(event.EstimatedRows), 0.99)
	}
	event.Percent = boundedPercent((float64(index) + fraction) / float64(len(m.plans)))
	return event
}

func boundedPercent(fraction float64) int {
	return max(0, min(100, int(math.Round(fraction*100))))
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
