package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Validação do corpo do POST /api/sessions/{sid}/onwhatsapp.
//
// A consulta em si vai ao servidor da Meta e não dá para exercitar aqui; o que
// este arquivo protege é o que acontece ANTES de sair uma consulta, que é onde
// moram as decisões que custam caro: teto por requisição, corpo limitado, e a
// recusa de número vazio.

func corpoDe(t *testing.T, v any) *http.Request {
	t.Helper()

	bruto, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return httptest.NewRequest(http.MethodPost, "/api/sessions/s1/onwhatsapp", bytes.NewReader(bruto))
}

func TestOnWhatsAppAceitaLoteValido(t *testing.T) {
	w := httptest.NewRecorder()
	phones, err := parseOnWhatsAppRequest(w, corpoDe(t, map[string]any{
		"phones": []string{"5514981120008", "+5514996035887"},
	}))

	if err != nil {
		t.Fatalf("lote válido recusado: %v", err)
	}
	if len(phones) != 2 {
		t.Errorf("queria 2 números, veio %d", len(phones))
	}
	// O número volta exatamente como veio, inclusive o `+`. É o que permite a
	// quem chamou casar a resposta com a linha da planilha dele.
	if phones[1] != "+5514996035887" {
		t.Errorf("o número foi alterado na entrada: %q", phones[1])
	}
}

func TestOnWhatsAppRecusaLoteGrandeDemais(t *testing.T) {
	// O IsOnWhatsApp é consulta USync ao servidor da Meta, e volume chama
	// atenção. O teto obriga quem importa a espaçar em vez de mandar a planilha
	// inteira de uma vez.
	muitos := make([]string, maxNumerosPorConsulta+1)
	for i := range muitos {
		muitos[i] = "5514981120008"
	}

	w := httptest.NewRecorder()
	if _, err := parseOnWhatsAppRequest(w, corpoDe(t, map[string]any{"phones": muitos})); err == nil {
		t.Errorf("lote de %d números passou, o teto é %d", len(muitos), maxNumerosPorConsulta)
	}

	// E exatamente no teto passa.
	w = httptest.NewRecorder()
	if _, err := parseOnWhatsAppRequest(w, corpoDe(t, map[string]any{"phones": muitos[:maxNumerosPorConsulta]})); err != nil {
		t.Errorf("lote no teto exato foi recusado: %v", err)
	}
}

func TestOnWhatsAppRecusaNumeroVazio(t *testing.T) {
	// Recusa o lote inteiro em vez de descartar a linha vazia. Descartar
	// desalinharia a resposta com a planilha de quem chamou, e o resultado de um
	// contato apareceria na linha de outro.
	for _, phones := range [][]string{
		{"5514981120008", ""},
		{"5514981120008", "   "},
	} {
		w := httptest.NewRecorder()
		if _, err := parseOnWhatsAppRequest(w, corpoDe(t, map[string]any{"phones": phones})); err == nil {
			t.Errorf("lote com número vazio passou: %q", phones)
		}
	}
}

func TestOnWhatsAppRecusaCorpoInutil(t *testing.T) {
	casos := []struct {
		nome  string
		corpo any
	}{
		{"lista vazia", map[string]any{"phones": []string{}}},
		{"sem o campo", map[string]any{}},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			w := httptest.NewRecorder()
			if _, err := parseOnWhatsAppRequest(w, corpoDe(t, caso.corpo)); err == nil {
				t.Error("corpo sem número para consultar foi aceito")
			}
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/onwhatsapp",
		bytes.NewReader([]byte("isto não é json")))

	if _, err := parseOnWhatsAppRequest(w, r); err == nil {
		t.Error("corpo que não é JSON foi aceito")
	}
}

func TestOnWhatsAppRecusaCorpoEnorme(t *testing.T) {
	// Sem teto de corpo, um POST de megabytes seria lido inteiro na memória
	// antes de o teto de 50 números sequer ser conferido.
	gigante := bytes.Repeat([]byte("a"), maxCorpoOnWhatsApp+1024)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/onwhatsapp", bytes.NewReader(gigante))

	if _, err := parseOnWhatsAppRequest(w, r); err == nil {
		t.Error("corpo acima do teto foi aceito")
	}
}
