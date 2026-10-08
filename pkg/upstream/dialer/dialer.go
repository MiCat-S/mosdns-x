/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package dialer

import (
	"context"
	"errors"
	"net"
)

type Dialer interface {
	DialContext(ctx context.Context, network string, addr string) (net.Conn, error)
}

type DialerOpts struct {
	Dialer     *net.Dialer
	SocksAddr  string
	S5Username string
	S5Password string
	// Proxy is an ss:// or socks5:// URL; see newProxyDialer.
	Proxy string
}

func NewDialer(opts DialerOpts) (Dialer, error) {
	switch {
	case opts.Proxy != "" && opts.SocksAddr != "":
		return nil, errors.New("set either proxy or socks5, not both")
	case opts.Proxy != "":
		return newProxyDialer(opts.Dialer, opts.Proxy)
	case opts.SocksAddr != "":
		return newSocksDialer(opts.Dialer, opts.SocksAddr, opts.S5Username, opts.S5Password)
	default:
		return newPlainDialer(opts.Dialer), nil
	}
}
