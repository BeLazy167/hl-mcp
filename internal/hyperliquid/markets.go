package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const builderDEXAssetOffset = 110000

type Market struct {
	Symbol      string
	Coin        string
	DEX         string
	Asset       uint64
	LocalIndex  int
	SizeDecimal int
	MaxLeverage int
	MinCostUSD  float64
}

type MarketSummary struct {
	Symbol       string  `json:"symbol"`
	Coin         string  `json:"coin"`
	DEX          string  `json:"dex"`
	AssetID      uint64  `json:"assetId"`
	SizeDecimals int     `json:"sizeDecimals"`
	MaxLeverage  int     `json:"maxLeverage"`
	MinCostUSD   float64 `json:"minCostUsd"`
}

type marketCatalog struct {
	byName  map[string]Market
	ordered []Market
}

type perpDEX struct {
	Name string `json:"name"`
}

type spotMeta struct {
	Tokens []struct {
		Name  string `json:"name"`
		Index int    `json:"index"`
	} `json:"tokens"`
}

type assetMeta struct {
	Name        string `json:"name"`
	SizeDecimal int    `json:"szDecimals"`
	MaxLeverage int    `json:"maxLeverage"`
}

type perpMeta struct {
	Universe        []assetMeta `json:"universe"`
	CollateralToken *int        `json:"collateralToken"`
}

func (c *Client) RefreshMarkets(ctx context.Context) error {
	var dexs []*perpDEX
	if err := c.info(ctx, map[string]any{"type": "perpDexs"}, &dexs); err != nil {
		return fmt.Errorf("load perp DEX list: %w", err)
	}
	offsets := map[string]uint64{"": 0}
	for index, dex := range dexs {
		if index == 0 || dex == nil {
			continue
		}
		offsets[strings.ToLower(dex.Name)] = builderDEXAssetOffset + uint64(index-1)*10000
	}
	for _, dex := range c.dexes {
		if _, ok := offsets[dex]; !ok {
			return fmt.Errorf("configured Hyperliquid DEX %q was not found", dex)
		}
	}

	var spots spotMeta
	if err := c.info(ctx, map[string]any{"type": "spotMeta"}, &spots); err != nil {
		return fmt.Errorf("load spot token metadata: %w", err)
	}
	quotes := make(map[int]string, len(spots.Tokens))
	for _, token := range spots.Tokens {
		quotes[token.Index] = token.Name
	}

	dexNames := append([]string{""}, c.dexes...)
	metas := make([]perpMeta, len(dexNames))
	errs := make([]error, len(dexNames))
	var wait sync.WaitGroup
	wait.Add(len(dexNames))
	for index, dex := range dexNames {
		go func() {
			defer wait.Done()
			metas[index], _, errs[index] = c.metaAndContexts(ctx, dex)
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			return fmt.Errorf("load metadata for DEX %q: %w", dexNames[index], err)
		}
	}

	catalog := &marketCatalog{byName: make(map[string]Market)}
	for dexIndex, dex := range dexNames {
		meta := metas[dexIndex]
		quote := "USDC"
		if dex != "" && meta.CollateralToken != nil {
			var ok bool
			quote, ok = quotes[*meta.CollateralToken]
			if !ok {
				return fmt.Errorf("DEX %q references unknown collateral token %d", dex, *meta.CollateralToken)
			}
		}
		for localIndex, asset := range meta.Universe {
			if asset.Name == "" || asset.SizeDecimal < 0 || asset.SizeDecimal > 8 {
				return fmt.Errorf("DEX %q returned invalid market metadata at index %d", dex, localIndex)
			}
			market := Market{
				Symbol:      displaySymbol(asset.Name, dex, quote),
				Coin:        asset.Name,
				DEX:         dex,
				Asset:       offsets[dex] + uint64(localIndex),
				LocalIndex:  localIndex,
				SizeDecimal: asset.SizeDecimal,
				MaxLeverage: asset.MaxLeverage,
				MinCostUSD:  10,
			}
			catalog.ordered = append(catalog.ordered, market)
			catalog.byName[strings.ToUpper(market.Symbol)] = market
			catalog.byName[strings.ToUpper(market.Coin)] = market
			if dex == "" {
				catalog.byName[strings.ToUpper(strings.Split(market.Symbol, "/")[0])] = market
			} else {
				base := strings.TrimPrefix(market.Coin, dex+":")
				catalog.byName[strings.ToUpper(dex+"-"+base)] = market
			}
		}
	}
	c.marketMu.Lock()
	c.markets = catalog
	c.marketMu.Unlock()
	return nil
}

func (c *Client) market(name string) (Market, error) {
	key := strings.ToUpper(strings.TrimSpace(name))
	c.marketMu.RLock()
	catalog := c.markets
	var market Market
	var ok bool
	if catalog != nil {
		market, ok = catalog.byName[key]
	}
	c.marketMu.RUnlock()
	if !ok {
		return Market{}, fmt.Errorf("unknown or unconfigured market %q", name)
	}
	return market, nil
}

func (c *Client) SearchMarkets(query string, limit int) ([]MarketSummary, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be from 1 through 100")
	}
	needle := strings.ToUpper(strings.TrimSpace(query))
	c.marketMu.RLock()
	catalog := c.markets
	if catalog == nil {
		c.marketMu.RUnlock()
		return nil, errors.New("market metadata is not loaded")
	}
	markets := append([]Market(nil), catalog.ordered...)
	c.marketMu.RUnlock()

	result := make([]MarketSummary, 0, min(limit, len(markets)))
	for _, market := range markets {
		if needle != "" && !strings.Contains(strings.ToUpper(market.Symbol), needle) {
			continue
		}
		result = append(result, MarketSummary{
			Symbol: market.Symbol, Coin: market.Coin, DEX: market.DEX, AssetID: market.Asset,
			SizeDecimals: market.SizeDecimal, MaxLeverage: market.MaxLeverage, MinCostUSD: market.MinCostUSD,
		})
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func displaySymbol(coin, dex, quote string) string {
	if dex == "" {
		return coin + "/" + quote + ":" + quote
	}
	base := strings.TrimPrefix(coin, dex+":")
	return strings.ToUpper(dex) + "-" + base + "/" + quote + ":" + quote
}

func (c *Client) metaAndContexts(ctx context.Context, dex string) (perpMeta, []json.RawMessage, error) {
	request := map[string]any{"type": "metaAndAssetCtxs"}
	if dex != "" {
		request["dex"] = dex
	}
	var raw []json.RawMessage
	if err := c.info(ctx, request, &raw); err != nil {
		return perpMeta{}, nil, err
	}
	if len(raw) != 2 {
		return perpMeta{}, nil, fmt.Errorf("metaAndAssetCtxs returned %d elements", len(raw))
	}
	var meta perpMeta
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return perpMeta{}, nil, fmt.Errorf("decode metadata: %w", err)
	}
	var contexts []json.RawMessage
	if err := json.Unmarshal(raw[1], &contexts); err != nil {
		return perpMeta{}, nil, fmt.Errorf("decode asset contexts: %w", err)
	}
	if len(meta.Universe) != len(contexts) {
		return perpMeta{}, nil, fmt.Errorf("metadata has %d markets but %d contexts", len(meta.Universe), len(contexts))
	}
	return meta, contexts, nil
}
