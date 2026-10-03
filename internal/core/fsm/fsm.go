// Package fsm — декларативная матрица «статус × команда» (спека 5.9, ADR-22).
// Интерфейсы не придумывают статусов и команд: допустимость решает только эта таблица.
package fsm

import "offgrid/core/internal/core/codes"

// Status — статус задачи (ADR-48).
type Status string

const (
	Draft      Status = "draft"
	Template   Status = "template"
	Pending    Status = "pending"
	Processing Status = "processing"
	Completed  Status = "completed"
	Failed     Status = "failed"
	Cancelled  Status = "cancelled"
)

// Statuses — полный справочник статусов задачи.
var Statuses = []Status{Draft, Template, Pending, Processing, Completed, Failed, Cancelled}

// Command — пользовательская команда по задаче.
type Command string

const (
	Cancel Command = "cancel"
	Retry  Command = "retry"
)

// Commands — полный справочник пользовательских команд.
var Commands = []Command{Cancel, Retry}

// guard решает, допустима ли команда в статусе с учётом вердикта.
type guard func(verdict codes.Code) bool

func always(codes.Code) bool { return true }

// matrix: (статус, команда) → условие. Отсутствие клетки = отказ.
var matrix = map[Status]map[Command]guard{
	Pending: {Cancel: always},
	// retry — только failed по вине исполнения; failed по данным — пересоздание бланка.
	Failed: {Retry: codes.Retryable},
}

// Allowed — можно ли применить команду.
func Allowed(s Status, c Command, verdict codes.Code) bool {
	g, ok := matrix[s][c]
	return ok && g(verdict)
}

// Known — команда есть в справочнике.
func Known(c Command) bool {
	for _, k := range Commands {
		if k == c {
			return true
		}
	}
	return false
}

// Terminal — статус завершённой задачи (неизменяема, ADR-14).
func Terminal(s Status) bool {
	return s == Completed || s == Failed || s == Cancelled
}

// Envelope — секция commands конверта для фронта: todo/disabled по матрице.
func Envelope(s Status, verdict codes.Code) map[string]map[string]string {
	out := make(map[string]map[string]string, len(Commands))
	for _, c := range Commands {
		if Allowed(s, c, verdict) {
			out[string(c)] = map[string]string{"status": "todo"}
		} else {
			out[string(c)] = map[string]string{"status": "disabled"}
		}
	}
	return out
}
