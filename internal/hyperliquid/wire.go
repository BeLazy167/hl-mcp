package hyperliquid

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type LimitOrderType struct {
	TIF string `json:"tif"`
}

type TriggerOrderType struct {
	IsMarket bool   `json:"isMarket"`
	Trigger  string `json:"triggerPx"`
	TPSL     string `json:"tpsl"`
}

type OrderType struct {
	Limit   *LimitOrderType   `json:"limit,omitempty"`
	Trigger *TriggerOrderType `json:"trigger,omitempty"`
}

type OrderWire struct {
	Asset      uint64    `json:"a"`
	IsBuy      bool      `json:"b"`
	Price      string    `json:"p"`
	Size       string    `json:"s"`
	ReduceOnly bool      `json:"r"`
	Type       OrderType `json:"t"`
	Cloid      string    `json:"c,omitempty"`
}

type OrderAction struct {
	Type     string      `json:"type"`
	Orders   []OrderWire `json:"orders"`
	Grouping string      `json:"grouping"`
}

type CancelWire struct {
	Asset uint64 `json:"a"`
	OID   uint64 `json:"o"`
}

type CancelAction struct {
	Type    string       `json:"type"`
	Cancels []CancelWire `json:"cancels"`
}

type UpdateLeverageAction struct {
	Type     string `json:"type"`
	Asset    uint64 `json:"asset"`
	IsCross  bool   `json:"isCross"`
	Leverage uint32 `json:"leverage"`
}

// marshalAction reproduces msgpack.packb from Hyperliquid's official Python SDK.
// Field order is part of the signed payload and must not change.
func marshalAction(action any) ([]byte, error) {
	p := new(msgPacker)
	switch value := action.(type) {
	case OrderAction:
		p.mapLen(3)
		p.string("type")
		p.string(value.Type)
		p.string("orders")
		p.arrayLen(len(value.Orders))
		for _, order := range value.Orders {
			fields := 6
			if order.Cloid != "" {
				fields++
			}
			p.mapLen(fields)
			p.string("a")
			p.uint(order.Asset)
			p.string("b")
			p.boolean(order.IsBuy)
			p.string("p")
			p.string(order.Price)
			p.string("s")
			p.string(order.Size)
			p.string("r")
			p.boolean(order.ReduceOnly)
			p.string("t")
			switch {
			case order.Type.Limit != nil && order.Type.Trigger == nil:
				p.mapLen(1)
				p.string("limit")
				p.mapLen(1)
				p.string("tif")
				p.string(order.Type.Limit.TIF)
			case order.Type.Trigger != nil && order.Type.Limit == nil:
				p.mapLen(1)
				p.string("trigger")
				p.mapLen(3)
				p.string("isMarket")
				p.boolean(order.Type.Trigger.IsMarket)
				p.string("triggerPx")
				p.string(order.Type.Trigger.Trigger)
				p.string("tpsl")
				p.string(order.Type.Trigger.TPSL)
			default:
				return nil, errors.New("order must have exactly one order type")
			}
			if order.Cloid != "" {
				p.string("c")
				p.string(order.Cloid)
			}
		}
		p.string("grouping")
		p.string(value.Grouping)
	case CancelAction:
		p.mapLen(2)
		p.string("type")
		p.string(value.Type)
		p.string("cancels")
		p.arrayLen(len(value.Cancels))
		for _, cancel := range value.Cancels {
			p.mapLen(2)
			p.string("a")
			p.uint(cancel.Asset)
			p.string("o")
			p.uint(cancel.OID)
		}
	case UpdateLeverageAction:
		p.mapLen(4)
		p.string("type")
		p.string(value.Type)
		p.string("asset")
		p.uint(value.Asset)
		p.string("isCross")
		p.boolean(value.IsCross)
		p.string("leverage")
		p.uint(uint64(value.Leverage))
	default:
		return nil, fmt.Errorf("unsupported action type %T", action)
	}
	return p.bytes(), nil
}

type msgPacker struct {
	data []byte
}

func (p *msgPacker) bytes() []byte { return p.data }

func (p *msgPacker) boolean(value bool) {
	if value {
		p.data = append(p.data, 0xc3)
		return
	}
	p.data = append(p.data, 0xc2)
}

func (p *msgPacker) uint(value uint64) {
	switch {
	case value <= 0x7f:
		p.data = append(p.data, byte(value))
	case value <= 0xff:
		p.data = append(p.data, 0xcc, byte(value))
	case value <= 0xffff:
		p.data = append(p.data, 0xcd, 0, 0)
		binary.BigEndian.PutUint16(p.data[len(p.data)-2:], uint16(value))
	case value <= 0xffffffff:
		p.data = append(p.data, 0xce, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(p.data[len(p.data)-4:], uint32(value))
	default:
		p.data = append(p.data, 0xcf, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(p.data[len(p.data)-8:], value)
	}
}

func (p *msgPacker) string(value string) {
	length := len(value)
	switch {
	case length <= 31:
		p.data = append(p.data, 0xa0|byte(length))
	case length <= 0xff:
		p.data = append(p.data, 0xd9, byte(length))
	case length <= 0xffff:
		p.data = append(p.data, 0xda, 0, 0)
		binary.BigEndian.PutUint16(p.data[len(p.data)-2:], uint16(length))
	default:
		p.data = append(p.data, 0xdb, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(p.data[len(p.data)-4:], uint32(length))
	}
	p.data = append(p.data, value...)
}

func (p *msgPacker) mapLen(length int) {
	if length < 0 || length > 15 {
		panic("msgpack map length outside supported range")
	}
	p.data = append(p.data, 0x80|byte(length))
}

func (p *msgPacker) arrayLen(length int) {
	switch {
	case length < 0:
		panic("negative msgpack array length")
	case length <= 15:
		p.data = append(p.data, 0x90|byte(length))
	case length <= 0xffff:
		p.data = append(p.data, 0xdc, 0, 0)
		binary.BigEndian.PutUint16(p.data[len(p.data)-2:], uint16(length))
	default:
		p.data = append(p.data, 0xdd, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(p.data[len(p.data)-4:], uint32(length))
	}
}
