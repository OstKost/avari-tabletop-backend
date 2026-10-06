// cmd/seed — загружает топ-10 популярных настольных игр RU-сегмента в базу данных.
//
// Режимы работы:
//   - По умолчанию: вставляет игры из встроенного каталога (без запросов к BGG).
//     Данные получены с BoardGameGeek заранее и встроены в бинарник.
//   - --live: обращается к BGG API в реальном времени (нужен доступ к boardgamegeek.com).
//
// Использование:
//
//	go run ./cmd/seed          # из встроенного каталога
//	go run ./cmd/seed --live   # из BGG API
//	make seed
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	tabletopai "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/config"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
)

// catalog — встроенные данные топ-10 игр RU-сегмента.
// Источник: BoardGameGeek (данные публичные, актуальны на апрель 2026).
var catalog = []model.Game{
	{
		BGGID:         13,
		Name:          "Catan",
		Description:   "Колонизаторы — классическая стратегическая игра на заселение острова Катан. Игроки собирают ресурсы, строят дороги, поселения и города, торгуют между собой и с банком. Побеждает первый, набравший 10 победных очков.",
		ThumbnailURL:  "https://cf.geekdo-images.com/W3Bsga_uLP9kO91gZ7H8yw__thumb/img/8a9HeqFydO7F7DOfFnEzgaVSzqU=/fit-in/200x150/filters:strip_icc()/pic2419375.jpg",
		ImageURL:      "https://cf.geekdo-images.com/W3Bsga_uLP9kO91gZ7H8yw__original/img/ETnDb98AvSMEFCkfzSnFMMMHVns=/0x0/filters:format(jpeg)/pic2419375.jpg",
		MinPlayers:    3,
		MaxPlayers:    4,
		PlayingTime:   120,
		YearPublished: 1995,
		AverageRating: 7.2,
	},
	{
		BGGID:         822,
		Name:          "Carcassonne",
		Description:   "Каркассон — тайловая стратегическая игра. Игроки выкладывают тайлы с городами, дорогами и монастырями, расставляют миплов, получая очки за завершённые объекты. Отлично подходит для 2–5 игроков.",
		ThumbnailURL:  "https://cf.geekdo-images.com/okiJBSfnvpOIPhYCGEKcXA__thumb/img/L5mmSiMbTjgdlkVVCjhPuMDQCyU=/fit-in/200x150/filters:strip_icc()/pic6544250.jpg",
		ImageURL:      "https://cf.geekdo-images.com/okiJBSfnvpOIPhYCGEKcXA__original/img/HRZKTcL3mz7bNDVpJQjABxkHqcY=/0x0/filters:format(jpeg)/pic6544250.jpg",
		MinPlayers:    2,
		MaxPlayers:    5,
		PlayingTime:   45,
		YearPublished: 2000,
		AverageRating: 7.4,
	},
	{
		BGGID:         14996,
		Name:          "Ticket to Ride: Europe",
		Description:   "Билет на поезд: Европа — строим железнодорожные маршруты по карте Европы. Игроки собирают карты вагонов, прокладывают пути между городами и выполняют маршрутные билеты. Знаковая семейная игра.",
		ThumbnailURL:  "https://cf.geekdo-images.com/O-GQXPSC7UNaqaFBvLQlpg__thumb/img/I9TBn7kUKVPsN3NnlUBJ2MfpDsQ=/fit-in/200x150/filters:strip_icc()/pic891608.jpg",
		ImageURL:      "https://cf.geekdo-images.com/O-GQXPSC7UNaqaFBvLQlpg__original/img/0F6yIYAB5exhBOETBEikNJHEhLc=/0x0/filters:format(jpeg)/pic891608.jpg",
		MinPlayers:    2,
		MaxPlayers:    5,
		PlayingTime:   90,
		YearPublished: 2005,
		AverageRating: 7.7,
	},
	{
		BGGID:         30549,
		Name:          "Pandemic",
		Description:   "Пандемия — кооперативная игра: команда специалистов ВОЗ сражается против четырёх болезней, распространяющихся по планете. Нужно найти лекарства от всех четырёх, пока не случилась катастрофа.",
		ThumbnailURL:  "https://cf.geekdo-images.com/S3ybV1LAJ-hglfQ6GDqdTg__thumb/img/LXqDjbAMSS6HOE7sJMiBJxlVJT4=/fit-in/200x150/filters:strip_icc()/pic1534148.jpg",
		ImageURL:      "https://cf.geekdo-images.com/S3ybV1LAJ-hglfQ6GDqdTg__original/img/zNXD5_JdStIxL-J7pOm9MUQM5QE=/0x0/filters:format(jpeg)/pic1534148.jpg",
		MinPlayers:    2,
		MaxPlayers:    4,
		PlayingTime:   45,
		YearPublished: 2008,
		AverageRating: 7.6,
	},
	{
		BGGID:         39856,
		Name:          "Dixit",
		Description:   "Диксит — игра на воображение и ассоциации. Рассказчик описывает карту с иллюстрацией одной фразой; остальные игроки выбирают из своих карт ту, что подходит лучше всего. Красочная, подходит для всей семьи.",
		ThumbnailURL:  "https://cf.geekdo-images.com/jRAbTQTHkYsqO_Y2bLmK0g__thumb/img/7FLRaBL-OWBDNF0QRwEW_BTuVbQ=/fit-in/200x150/filters:strip_icc()/pic3483909.jpg",
		ImageURL:      "https://cf.geekdo-images.com/jRAbTQTHkYsqO_Y2bLmK0g__original/img/Y93Y7Gg5NKFrNV9BnmE8v2AFNLY=/0x0/filters:format(jpeg)/pic3483909.jpg",
		MinPlayers:    3,
		MaxPlayers:    6,
		PlayingTime:   30,
		YearPublished: 2008,
		AverageRating: 7.2,
	},
	{
		BGGID:         178900,
		Name:          "Codenames",
		Description:   "Кодовые имена — командная игра на слова. Два капитана дают однословные подсказки, чтобы их команда угадала слова-агентов на поле. Не угадай слово противника и не откройте Чёрного агента!",
		ThumbnailURL:  "https://cf.geekdo-images.com/F_KDEu0GjdClml8N7c8Imw__thumb/img/iJPkyqT-JbJcgzFwRlJWMFZ4BJA=/fit-in/200x150/filters:strip_icc()/pic2582929.jpg",
		ImageURL:      "https://cf.geekdo-images.com/F_KDEu0GjdClml8N7c8Imw__original/img/acJNPZZEL0LFzXvVK_QnYfTRpwg=/0x0/filters:format(jpeg)/pic2582929.jpg",
		MinPlayers:    2,
		MaxPlayers:    8,
		PlayingTime:   15,
		YearPublished: 2015,
		AverageRating: 7.7,
	},
	{
		BGGID:         68448,
		Name:          "7 Wonders",
		Description:   "7 Чудес — карточная игра с драфтом. За три эпохи игроки строят древние цивилизации, развивают науку, армию, торговлю и создают одно из семи чудес света. До 7 игроков, партия занимает около 30 минут.",
		ThumbnailURL:  "https://cf.geekdo-images.com/RvFVTEpnbb4NM7k0IF8V7A__thumb/img/zMKCZK-GpZKLWyXQ1cOSFUYFECw=/fit-in/200x150/filters:strip_icc()/pic860217.jpg",
		ImageURL:      "https://cf.geekdo-images.com/RvFVTEpnbb4NM7k0IF8V7A__original/img/qJREGPKVPZnEbpJLrGXQRb05_5Y=/0x0/filters:format(jpeg)/pic860217.jpg",
		MinPlayers:    2,
		MaxPlayers:    7,
		PlayingTime:   30,
		YearPublished: 2010,
		AverageRating: 7.7,
	},
	{
		BGGID:         1927,
		Name:          "Munchkin",
		Description:   "Манчкин — карточная пародия на RPG. Игроки исследуют подземелья, побеждают монстров, собирают шмот и мешают друг другу. Первый, достигший 10-го уровня, побеждает. Легко учится, бесконечно весёлая.",
		ThumbnailURL:  "https://cf.geekdo-images.com/GeOg7IFBiohEA_sz-kcKAA__thumb/img/3tRX1LKZJ25E6wOGtF-E7Yb68cU=/fit-in/200x150/filters:strip_icc()/pic4462008.jpg",
		ImageURL:      "https://cf.geekdo-images.com/GeOg7IFBiohEA_sz-kcKAA__original/img/X1RR4INE-bUXdDivFuZVf0TNQCE=/0x0/filters:format(jpeg)/pic4462008.jpg",
		MinPlayers:    3,
		MaxPlayers:    6,
		PlayingTime:   120,
		YearPublished: 2001,
		AverageRating: 6.9,
	},
	{
		BGGID:         36218,
		Name:          "Dominion",
		Description:   "Доминион — родоначальник жанра «deck-building». Игроки начинают с одинаковой колодой и покупают новые карты действий, чтобы построить эффективную машину для набора победных очков.",
		ThumbnailURL:  "https://cf.geekdo-images.com/j4TETYS-_KumJEOBuHXOxw__thumb/img/b1TbAFqHuAKfT1GqaQuFcFaEWKg=/fit-in/200x150/filters:strip_icc()/pic394356.jpg",
		ImageURL:      "https://cf.geekdo-images.com/j4TETYS-_KumJEOBuHXOxw__original/img/yq1WqPqMPb8jmVa_WeSLqFbFjcQ=/0x0/filters:format(jpeg)/pic394356.jpg",
		MinPlayers:    2,
		MaxPlayers:    4,
		PlayingTime:   30,
		YearPublished: 2008,
		AverageRating: 7.6,
	},
	{
		BGGID:         28199,
		Name:          "Alias",
		Description:   "Элиас — командная игра на объяснение слов. Один игрок объясняет слова с карточки без использования однокоренных слов; команда угадывает как можно больше за отведённое время. Весело в любой компании.",
		ThumbnailURL:  "https://cf.geekdo-images.com/OFIWfTtVJVF7T4Vh5yGUYA__thumb/img/yVTkgOxN-oFuSqMKNalr2mHfnqE=/fit-in/200x150/filters:strip_icc()/pic156547.jpg",
		ImageURL:      "https://cf.geekdo-images.com/OFIWfTtVJVF7T4Vh5yGUYA__original/img/Y6bSt6L2oH0TfCtPc5Z4MHLqt8g=/0x0/filters:format(jpeg)/pic156547.jpg",
		MinPlayers:    4,
		MaxPlayers:    12,
		PlayingTime:   45,
		YearPublished: 2000,
		AverageRating: 6.3,
	},
}

func main() {
	live := flag.Bool("live", false, "fetch fresh data from BGG API instead of built-in catalog")
	flag.Parse()

	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := repository.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	migsFS, err := fs.Sub(tabletopai.Migrations, "migrations")
	if err != nil {
		slog.Error("prepare migrations fs", "error", err)
		os.Exit(1)
	}
	if err := repository.RunMigrations(ctx, pool, migsFS, "003_pgvector", "008_private_pgvector"); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}

	gameRepo := repository.NewGameRepository(pool)

	if *live {
		seedFromBGG(ctx, gameRepo, cfg.BGGAPIToken)
	} else {
		seedFromCatalog(ctx, gameRepo)
	}
}

func seedFromCatalog(ctx context.Context, gameRepo repository.GameRepository) {
	slog.Info("seeding from built-in catalog", "count", len(catalog))
	ok := 0
	for _, g := range catalog {
		saved, err := gameRepo.Upsert(ctx, g)
		if err != nil {
			slog.Error("upsert game", "bgg_id", g.BGGID, "name", g.Name, "error", err)
			continue
		}
		slog.Info("✓ saved", "id", saved.ID, "name", saved.Name, "year", saved.YearPublished,
			"rating", fmt.Sprintf("%.1f", saved.AverageRating))
		ok++
	}
	slog.Info("seed complete", "saved", ok, "total", len(catalog))
}

func seedFromBGG(ctx context.Context, gameRepo repository.GameRepository, token string) {
	client := bgg.NewClientWithToken(token)
	slog.Info("seeding from BGG API (live)", "count", len(catalog))
	ok, failed := 0, 0
	for _, entry := range catalog {
		slog.Info("fetching from BGG", "bgg_id", entry.BGGID, "name", entry.Name)
		detail, err := client.GetGame(ctx, entry.BGGID)
		if err != nil {
			slog.Error("fetch from BGG", "bgg_id", entry.BGGID, "error", err)
			failed++
			continue
		}
		g := model.Game{
			BGGID:         detail.BGGID,
			Name:          detail.Name,
			Description:   detail.Description,
			ImageURL:      detail.ImageURL,
			ThumbnailURL:  detail.ThumbnailURL,
			MinPlayers:    detail.MinPlayers,
			MaxPlayers:    detail.MaxPlayers,
			PlayingTime:   detail.PlayingTime,
			YearPublished: detail.YearPublished,
			AverageRating: detail.AverageRating,
		}
		saved, err := gameRepo.Upsert(ctx, g)
		if err != nil {
			slog.Error("upsert game", "bgg_id", g.BGGID, "error", err)
			failed++
			continue
		}
		slog.Info("✓ saved", "id", saved.ID, "name", saved.Name, "rating", fmt.Sprintf("%.1f", saved.AverageRating))
		ok++
	}
	slog.Info("seed complete", "saved", ok, "failed", failed)
	if failed > 0 {
		os.Exit(1)
	}
}
