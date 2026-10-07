package web

import (
	"fmt"
	"math"
	"strings"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

const (
	dashboardDays   = 14
	chartWidth      = 720
	chartHeight     = 220
	chartLeftPad    = 48
	chartRightPad   = 16
	chartTopPad     = 16
	chartBottomPad  = 36
	chartMinScale   = 1.0
	chartLabelCount = 7
)

type ChartView struct {
	Caption  string
	Unit     string
	Points   string
	MaxLabel string
	Ticks    []ChartTick
	Width    int
	Height   int
	AxisY    float64
	AxisX    float64
}

type ChartTick struct {
	Label string
	X     float64
}

func newChart(caption, unit string, series []storage.MetricPoint) ChartView {
	plotWidth := float64(chartWidth - chartLeftPad - chartRightPad)
	plotHeight := float64(chartHeight - chartTopPad - chartBottomPad)
	axisY := float64(chartTopPad) + plotHeight
	axisX := float64(chartLeftPad)
	rawMax := 0.0
	for index := range series {
		if series[index].Value > rawMax {
			rawMax = series[index].Value
		}
	}
	scale := max(rawMax, chartMinScale)
	span := max(len(series)-1, 1)
	points := make([]string, 0, len(series))
	ticks := make([]ChartTick, 0, chartLabelCount)
	tickStep := max(len(series)/chartLabelCount, 1)
	for index := range series {
		x := axisX + plotWidth*float64(index)/float64(span)
		y := float64(chartTopPad) + plotHeight*(1-series[index].Value/scale)
		points = append(points, fmt.Sprintf("%.1f,%.1f", x, y))
		if index%tickStep == 0 || index == len(series)-1 {
			ticks = append(ticks, ChartTick{Label: series[index].Day, X: x})
		}
	}
	return ChartView{
		Caption:  caption,
		Unit:     unit,
		Points:   strings.Join(points, " "),
		MaxLabel: formatChartMax(rawMax, unit),
		Ticks:    ticks,
		Width:    chartWidth,
		Height:   chartHeight,
		AxisY:    axisY,
		AxisX:    axisX,
	}
}

func formatChartMax(value float64, unit string) string {
	label := fmt.Sprintf("%.0f", math.Ceil(value))
	if unit != "" {
		return label + " " + unit
	}
	return label
}
