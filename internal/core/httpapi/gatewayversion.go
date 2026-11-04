package httpapi

import (
	"net/http"

	"offgrid/core/internal/core/contracts"
)

// Версия договора шлюза задач (ADR-82) — из самого файла договора: цифра в коде не разойдётся с договором.
var coreGateway = func() string {
	v, err := contracts.InterfaceVersion(contracts.GatewayFile)
	if err != nil {
		panic(err)
	}
	return v
}()

// gatewayVersion — ядро говорит прорабу версию договора шлюза в каждом ответе (X-Core-Gateway). Прораб, назвавший
// в X-Worker-Gateway другую старшую версию, получает понятный отказ 409 gateway_version, а не странную ошибку
// позже. Без заголовка — как раньше: старые прорабы работают.
func gatewayVersion(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Core-Gateway", coreGateway)
		if want := r.Header.Get("X-Worker-Gateway"); want != "" && contracts.Major(want) != contracts.Major(coreGateway) {
			writeError(w, http.StatusConflict, "gateway_version",
				"worker is built for gateway contract "+want+", core speaks "+coreGateway+": update the worker SDK")
			return
		}
		h(w, r)
	}
}
