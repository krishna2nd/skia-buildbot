package calc

import (
	"fmt"
	"math"
	"strconv"

	"go.skia.org/infra/go/vec32"
)

const (
	// MIN_STDDEV is the smallest standard deviation we will normalize, smaller
	// than this and we presume it's a standard deviation of zero.
	MIN_STDDEV = 0.001
)

type FilterFunc struct{}

// filterFunc is a Func that returns a filtered set of Rows in the Context.
//
// It expects a single argument that is a string in URL query format, ala
// os=Ubuntu12&config=8888.
func (FilterFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("filter() takes a single argument.")
	}
	if node.Args[0].Typ != NodeString {
		return nil, fmt.Errorf("filter() takes a string argument.")
	}
	return ctx.RowsFromQuery(node.Args[0].Val)
}

func (FilterFunc) Describe() string {
	return `filter() returns a filtered set of Rows that match the given query.

  It expects a single argument that is a string in URL query format, such as:

     os=Ubuntu12&config=8888.`
}

var filterFunc = FilterFunc{}

type NormFunc struct{}

// normFunc implements Func and normalizes the traces to a mean of 0 and a
// standard deviation of 1.0. If a second optional number is passed in to
// norm() then that is used as the minimum standard deviation that is
// normalized, otherwise it defaults to MIN_STDDEV.
func (NormFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) > 2 || len(node.Args) == 0 {
		return nil, fmt.Errorf("norm() takes one or two arguments.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("norm() takes a function as its first argument.")
	}
	minStdDev := MIN_STDDEV
	if len(node.Args) == 2 {
		if node.Args[1].Typ != NodeNum {
			return nil, fmt.Errorf("norm() takes a number as its second argument.")
		}
		var err error
		minStdDev, err = strconv.ParseFloat(node.Args[1].Val, 32)
		if err != nil {
			return nil, fmt.Errorf("norm() stddev not a valid number %s : %s", node.Args[1].Val, err)
		}
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("norm() failed evaluating argument: %s", err)
	}

	ret := Rows{}
	for key, r := range rows {
		row := vec32.Dup(r)
		vec32.Norm(row, float32(minStdDev))
		ret["norm("+key+")"] = row
	}

	return ret, nil
}

func (NormFunc) Describe() string {
	return `norm() normalizes the rows to a mean of 0 and a standard deviation of 1.0.

  If a second optional number is passed in to
  norm() then that is used as the minimum standard deviation that is
  normalized, otherwise it defaults to 0.1.`
}

var normFunc = NormFunc{}

type FillFunc struct{}

// fillFunc implements Func and fills in all the missing datapoints with nearby
// points.
//
// Note that a Row with all vec32.MISSING_DATA_SENTINEL values will be filled with
// 0's.
func (FillFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("fill() takes a single argument.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("fill() takes a function argument.")
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("fill() failed evaluating argument: %s", err)
	}

	ret := Rows{}
	for key, r := range rows {
		row := vec32.Dup(r)
		vec32.Fill(row)
		ret["fill("+key+")"] = row
	}
	return ret, nil
}

func (FillFunc) Describe() string {
	return `fill() fills in all the missing datapoints with nearby points.`
}

var fillFunc = FillFunc{}

type AveFunc struct{}

// aveFunc implements Func and averages the values of all argument
// traces into a single trace.
//
// vec32.MISSING_DATA_SENTINEL values are not included in the average.  Note that if
// all the values at an index are vec32.MISSING_DATA_SENTINEL then the average will
// be vec32.MISSING_DATA_SENTINEL.
func (AveFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("ave() takes a single argument.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("ave() takes a function argument.")
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("ave() argument failed to evaluate: %s", err)
	}

	if len(rows) == 0 {
		return rows, nil
	}

	ret := newRow(rows)
	for i, _ := range ret {
		sum := float32(0.0)
		count := 0
		for _, r := range rows {
			if v := r[i]; v != vec32.MISSING_DATA_SENTINEL {
				sum += v
				count += 1
			}
		}
		if count > 0 {
			ret[i] = sum / float32(count)
		}
	}
	return Rows{ctx.formula: ret}, nil
}

func (AveFunc) Describe() string {
	return `ave() averages the values of all argument rows into a single trace.`
}

var aveFunc = AveFunc{}

type RatioFunc struct{}

func (RatioFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 2 {
		return nil, fmt.Errorf("ratio() takes two arguments")
	}

	rowsA, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("ratio() argument failed to evaluate: %s", err)
	}
	rowA := []float32{}
	for _, v := range rowsA {
		rowA = v
		break
	}

	rowsB, err := node.Args[1].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("ratio() argument failed to evaluate: %s", err)
	}
	rowB := []float32{}
	for _, v := range rowsB {
		rowB = v
		break
	}

	ret := newRow(rowsA)
	for i, _ := range ret {
		ret[i] = rowA[i] / rowB[i]
		if math.IsInf(float64(ret[i]), 0) {
			ret[i] = vec32.MISSING_DATA_SENTINEL
		}
	}
	return Rows{ctx.formula: ret}, nil
}

func (RatioFunc) Describe() string {
	return `ratio(a, b) returns the point by point ratio of two rows.
                That is, it returns a trace with a[i]/b[i] for every point in a and b.`
}

var ratioFunc = RatioFunc{}

// CountFunc implements Func and counts the number of non-sentinel values in
// all argument rows.
//
// vec32.MISSING_DATA_SENTINEL values are not included in the count.  Note that if
// all the values at an index are vec32.MISSING_DATA_SENTINEL then the count will
// be 0.
type CountFunc struct{}

func (CountFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("count() takes a single argument.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("count() takes a function argument.")
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("count() argument failed to evaluate: %s", err)
	}

	if len(rows) == 0 {
		return rows, nil
	}

	ret := newRow(rows)
	for i, _ := range ret {
		count := 0
		for _, r := range rows {
			if r[i] != vec32.MISSING_DATA_SENTINEL {
				count += 1
			}
		}
		ret[i] = float32(count)
	}
	return Rows{ctx.formula: ret}, nil
}

func (CountFunc) Describe() string {
	return `count() counts the non-missing values of all argument rows.`
}

var countFunc = CountFunc{}

type SumFunc struct{}

// SumFunc implements Func and sums the values of all argument
// rows into a single trace.
//
// vec32.MISSING_DATA_SENTINEL values are not included in the sum. Note that if all
// the values at an index are vec32.MISSING_DATA_SENTINEL then the sum will be
// vec32.MISSING_DATA_SENTINEL.
func (SumFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("sum() takes a single argument.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("sum() takes a function argument.")
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("Sum() argument failed to evaluate: %s", err)
	}

	if len(rows) == 0 {
		return rows, nil
	}

	ret := newRow(rows)
	for i, _ := range ret {
		sum := float32(0.0)
		count := 0
		for _, r := range rows {
			if v := r[i]; v != vec32.MISSING_DATA_SENTINEL {
				sum += v
				count += 1
			}
		}
		if count > 0 {
			ret[i] = sum
		}
	}
	return Rows{ctx.formula: ret}, nil
}

func (SumFunc) Describe() string {
	return `Sum() Sums the values of all argument rows into a single trace.`
}

var sumFunc = SumFunc{}

type GeoFunc struct{}

// geoFunc implements Func and merges the values of all argument
// rows into a single trace with a geometric mean.
//
// vec32.MISSING_DATA_SENTINEL and negative values are not included in the mean.
// Note that if all the values at an index are vec32.MISSING_DATA_SENTINEL or
// negative then the mean will be vec32.MISSING_DATA_SENTINEL.
func (GeoFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("geo() takes a single argument.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("geo() takes a function argument.")
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("geo() argument failed to evaluate: %s", err)
	}

	if len(rows) == 0 {
		return rows, nil
	}

	ret := newRow(rows)
	for i, _ := range ret {
		// We're accumulating a product, but in log-space to avoid large N overflow.
		sumLog := 0.0
		count := 0
		for _, r := range rows {
			if v := r[i]; v >= 0 && v != vec32.MISSING_DATA_SENTINEL {
				sumLog += math.Log(float64(v))
				count += 1
			}
		}
		if count > 0 {
			// The geometric mean is the N-th root of the product of N terms.
			// In log-space, the root becomes a division, then we translate back to normal space.
			ret[i] = float32(math.Exp(sumLog / float64(count)))
		}
	}
	return Rows{ctx.formula: ret}, nil
}

func (GeoFunc) Describe() string {
	return `geo() folds the values of all argument rows into a single geometric mean trace.`
}

var geoFunc = GeoFunc{}

type LogFunc struct{}

// logFunc implements Func and transforms a row of x into a row of log10(x).
//
// Values <= 0 are set to vec32.MISSING_DATA_SENTINEL.  vec32.MISSING_DATA_SENTINEL values are left untouched.
func (LogFunc) Eval(ctx *Context, node *Node) (Rows, error) {
	if len(node.Args) != 1 {
		return nil, fmt.Errorf("log() takes a single argument.")
	}
	if node.Args[0].Typ != NodeFunc {
		return nil, fmt.Errorf("log() takes a function argument.")
	}
	rows, err := node.Args[0].Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("log() failed evaluating argument: %s", err)
	}

	for j, r := range rows {
		row := vec32.Dup(r)
		for i, v := range row {
			if v != vec32.MISSING_DATA_SENTINEL {
				if v > 0 {
					row[i] = float32(math.Log10(float64(v)))
				} else {
					row[i] = vec32.MISSING_DATA_SENTINEL
				}
			}
		}
		rows[j] = row
	}
	// TODO rename the rows
	return rows, nil
}

func (LogFunc) Describe() string {
	return `log() applies a base-10 logarithm to the datapoints.`
}

var logFunc = LogFunc{}
