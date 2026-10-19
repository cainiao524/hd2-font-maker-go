package mygoapp

// New native-shell statuses; existing catalog files remain byte-for-byte copies.
var nativeMessages = map[string][4]string{
	"en":     {"Generation backend not connected", "Cancelled", "Cancelling...", "DEMO - no game package"},
	"fr":     {"Moteur de génération non connecté", "Annulé", "Annulation...", "DÉMO - aucun paquet de jeu"},
	"it":     {"Motore di generazione non collegato", "Annullato", "Annullamento...", "DEMO - nessun pacchetto di gioco"},
	"de":     {"Generierungsmodul nicht verbunden", "Abgebrochen", "Wird abgebrochen...", "DEMO - kein Spielpaket"},
	"es-ES":  {"Motor de generación no conectado", "Cancelado", "Cancelando...", "DEMO - sin paquete de juego"},
	"ja":     {"生成エンジンが接続されていません", "キャンセルしました", "キャンセル中...", "デモ - ゲーム用パッケージなし"},
	"ko":     {"생성 엔진이 연결되지 않았습니다", "취소됨", "취소 중...", "데모 - 게임 패키지 없음"},
	"pt-BR":  {"Mecanismo de geração não conectado", "Cancelado", "Cancelando...", "DEMO - sem pacote de jogo"},
	"es-419": {"Motor de generación no conectado", "Cancelado", "Cancelando...", "DEMO - sin paquete de juego"},
	"pl":     {"Silnik generowania nie jest podłączony", "Anulowano", "Anulowanie...", "DEMO - bez pakietu gry"},
	"pt-PT":  {"Motor de geração não ligado", "Cancelado", "A cancelar...", "DEMO - sem pacote de jogo"},
	"ru":     {"Модуль генерации не подключён", "Отменено", "Отмена...", "ДЕМО - без игрового пакета"},
	"zh-CN":  {"生成后端尚未连接", "已取消", "正在取消...", "演示模式 - 不生成游戏字体包"},
	"zh-TW":  {"產生後端尚未連接", "已取消", "正在取消...", "示範模式 - 不產生遊戲字型包"},
}
