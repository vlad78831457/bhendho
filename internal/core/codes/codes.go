// Package codes — единый реестр кодов отказа (спека 5.10, ADR-24, ADR-48).
// Код без строки реестра не существует: у каждого есть этап, «кто лечит»,
// класс (определяет retry) и шаблон сообщения пользователю.
package codes

// Code — вердикт задачи: SUCCESS либо код отказа.
type Code string

const (
	Success           Code = "SUCCESS"
	InsufficientFunds Code = "INSUFFICIENT_FUNDS"
	ValidationError   Code = "VALIDATION_ERROR"
	FactNotPublished  Code = "FACT_NOT_PUBLISHED"
	FactMissing       Code = "FACT_MISSING"
	FileMissing       Code = "FILE_MISSING"
	WorkerTimeout     Code = "WORKER_TIMEOUT"
	LimitExhausted    Code = "LIMIT_EXHAUSTED"
	WorkerError       Code = "WORKER_ERROR"
	ReportInvalid     Code = "REPORT_INVALID"
	CommandRefused    Code = "COMMAND_REFUSED"
	DataRejected      Code = "DATA_REJECTED"
)

// Healer — кто лечит.
type Healer string

const (
	HealerSystem Healer = "system"
	HealerAdmin  Healer = "admin"
	HealerUser   Healer = "user"
)

// Class — класс отказа; определяет retry по матрице (ADR-22).
type Class string

const (
	ClassData      Class = "data"      // вина данных — retry нет
	ClassExecution Class = "execution" // вина исполнения — retry есть
	ClassTransient Class = "transient" // система лечит сама
	ClassNone      Class = "none"
)

// Entry — строка реестра.
type Entry struct {
	Code    Code
	Stage   string
	Healer  Healer
	Class   Class
	Message string // шаблон пользователю: что / почему / где / когда
}

var registry = map[Code]Entry{
	Success:           {Success, "result", HealerSystem, ClassNone, "Готово"},
	InsufficientFunds: {InsufficientFunds, "create", HealerUser, ClassData, "Не хватает средств — пополните кошелёк"},
	ValidationError:   {ValidationError, "create", HealerUser, ClassData, "Поле не прошло проверку"},
	FactNotPublished:  {FactNotPublished, "create", HealerUser, ClassData, "Факт ещё не опубликован"},
	FactMissing:       {FactMissing, "queue", HealerUser, ClassData, "Факт пропал до старта, резерв снят"},
	FileMissing:       {FileMissing, "queue", HealerUser, ClassData, "Файл пропал до старта, резерв снят"},
	WorkerTimeout:     {WorkerTimeout, "execution", HealerSystem, ClassTransient, "Исполнитель не ответил, задача вернулась в очередь"},
	LimitExhausted:    {LimitExhausted, "execution", HealerAdmin, ClassExecution, "Попытки кончились, резерв снят"},
	WorkerError:       {WorkerError, "execution", HealerAdmin, ClassExecution, "Исполнитель сообщил об ошибке, резерв снят"},
	ReportInvalid:     {ReportInvalid, "execution", HealerAdmin, ClassExecution, "Отчёт исполнителя не прошёл проверку, резерв снят"},
	CommandRefused:    {CommandRefused, "command", HealerUser, ClassNone, "Команду нельзя применить к задаче в этом статусе"},
	DataRejected:      {DataRejected, "execution", HealerUser, ClassData, "Исполнитель не может обработать эти данные — исправьте и отправьте заново"},
}

// Lookup возвращает строку реестра; ok=false — кода не существует.
func Lookup(c Code) (Entry, bool) {
	e, ok := registry[c]
	return e, ok
}

// Known — код есть в реестре.
func Known(c Code) bool {
	_, ok := registry[c]
	return ok
}

// Retryable — failed с этим кодом допускает команду retry (вина исполнения, ADR-22).
func Retryable(c Code) bool {
	e, ok := registry[c]
	return ok && e.Class == ClassExecution
}

// All — все коды реестра (для тестов и схем).
func All() []Code {
	out := make([]Code, 0, len(registry))
	for c := range registry {
		out = append(out, c)
	}
	return out
}
