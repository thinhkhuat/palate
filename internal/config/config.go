package config

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DBPath            string             `yaml:"db_path"`
	CFloor            float64            `yaml:"c_floor"`
	HalfLifeHours     float64            `yaml:"half_life_hours"`
	LearningRate      float64            `yaml:"learning_rate"`
	Weights           Weights            `yaml:"weights"`
	TokenBudgets      TokenBudgets       `yaml:"token_budgets"`
	Freshness         Freshness          `yaml:"freshness"`
	Hybrid            Hybrid             `yaml:"hybrid"`
	TaxonomyThreshold int                `yaml:"taxonomy_threshold"`
	MCP               MCP                `yaml:"mcp"`
}

type Weights struct {
	ExplicitCorrection float64 `yaml:"explicit_correction"`
	UserDiff           float64 `yaml:"user_diff"`
	ExplicitReject     float64 `yaml:"explicit_reject"`
	StatedPreference   float64 `yaml:"stated_preference"`
	ExplicitPrefer     float64 `yaml:"explicit_prefer"`
	ExplicitAccept     float64 `yaml:"explicit_accept"`
	ModelEdit          float64 `yaml:"model_edit"`
	Imported           float64 `yaml:"imported"`
}

type TokenBudgets struct {
	PrewarmSession int `yaml:"prewarm_session"`
	PrewarmGoal    int `yaml:"prewarm_goal"`
	Recall         int `yaml:"recall"`
	IndexLines     int `yaml:"index_lines"`
	IndexBytes     int `yaml:"index_bytes"`
}

type Freshness struct {
	Enabled       bool    `yaml:"enabled"`
	T1Days        int     `yaml:"t1_days"`
	T1Weight      float64 `yaml:"t1_weight"`
	T2Days        int     `yaml:"t2_days"`
	T2Weight      float64 `yaml:"t2_weight"`
	T3Weight      float64 `yaml:"t3_weight"`
	CompressChars int     `yaml:"compress_chars"`
}

type Hybrid struct {
	Vec        float64 `yaml:"vec"`
	FTS        float64 `yaml:"fts"`
	Importance float64 `yaml:"importance"`
	RRFK       int     `yaml:"rrf_k"`
}

type MCP struct {
	TokenEnv  string   `yaml:"token_env"`
	Allowlist []string `yaml:"allowlist"`
}

func Default() Config {
	return Config{
		DBPath:        ".palate/palate.db",
		CFloor:        0.1,
		HalfLifeHours: 168,
		LearningRate:  0.3,
		Weights: Weights{
			ExplicitCorrection: 1.0,
			UserDiff:           0.6,
			ExplicitReject:     0.9,
			StatedPreference:   0.8,
			ExplicitPrefer:     0.8,
			ExplicitAccept:     0.6,
			ModelEdit:          0.4,
			Imported:           0.4,
		},
		TokenBudgets: TokenBudgets{
			PrewarmSession: 500,
			PrewarmGoal:    200,
			Recall:         1000,
			IndexLines:     200,
			IndexBytes:     25600,
		},
		Freshness: Freshness{
			Enabled:       false,
			T1Days:        30,
			T1Weight:      1.0,
			T2Days:        90,
			T2Weight:      0.5,
			T3Weight:      0.25,
			CompressChars: 300,
		},
		Hybrid: Hybrid{
			Vec:        0.5,
			FTS:        0.3,
			Importance: 0.2,
			RRFK:       60,
		},
		TaxonomyThreshold: 5,
		MCP: MCP{
			TokenEnv:  "PALATE_MCP_TOKEN",
			Allowlist: []string{"127.0.0.1", "localhost", "::1", "ip6-localhost"},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnv(&cfg)
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	applyEnv(&cfg)
	return cfg, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("PALATE_DB_PATH"); v != "" {
		c.DBPath = v
	}
	if v := os.Getenv("PALATE_C_FLOOR"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.CFloor = f
		}
	}
	if v := os.Getenv("PALATE_HALF_LIFE_HOURS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.HalfLifeHours = f
		}
	}
}

func (c Config) WeightFor(signal string) float64 {
	switch signal {
	case "explicit_correction":
		return c.Weights.ExplicitCorrection
	case "user_diff":
		return c.Weights.UserDiff
	case "explicit_reject":
		return c.Weights.ExplicitReject
	case "stated_preference":
		return c.Weights.StatedPreference
	case "explicit_prefer":
		return c.Weights.ExplicitPrefer
	case "explicit_accept":
		return c.Weights.ExplicitAccept
	case "model_edit":
		return c.Weights.ModelEdit
	case "imported":
		return c.Weights.Imported
	default:
		return c.Weights.StatedPreference
	}
}
