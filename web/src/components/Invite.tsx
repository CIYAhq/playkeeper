import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { ApiError, del, get, post } from '../api/client'
import type { WhitelistEntry } from '../api/types'
import { Banner } from './ui'

/** Adds Java Edition usernames to the server's allowlist (audited). */
export function InviteFriends({ online }: { online: boolean }) {
  const [list, setList] = useState<WhitelistEntry[]>()
  const [name, setName] = useState('')
  const [msg, setMsg] = useState<{ tone: 'good' | 'bad'; text: string; hint?: string }>()
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setList(await get<WhitelistEntry[]>('/api/server/whitelist'))
    } catch {
      setList(undefined)
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  async function add(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setMsg(undefined)
    try {
      const r = await post<{ message: string; whitelist: WhitelistEntry[] }>('/api/server/whitelist', { name: name.trim() })
      setMsg({ tone: 'good', text: r.message })
      setList(r.whitelist)
      setName('')
    } catch (err) {
      const e2 = err as ApiError
      setMsg({ tone: 'bad', text: e2.message, hint: e2.hint })
    } finally {
      setBusy(false)
    }
  }

  async function remove(n: string) {
    try {
      await del(`/api/server/whitelist/${encodeURIComponent(n)}`)
      await load()
    } catch (err) {
      setMsg({ tone: 'bad', text: (err as ApiError).message })
    }
  }

  return (
    <div className="stack">
      <p className="muted small">Only players on this list can join. Add your friends' Java Edition usernames, then send them the join address.</p>
      <form className="row" onSubmit={add}>
        <div className="field">
          <label htmlFor="invite-name">Minecraft username</label>
          <input id="invite-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Notch" pattern="[A-Za-z0-9_]{3,16}" title="3–16 letters, numbers or underscores" required disabled={!online} />
        </div>
        <button className="btn primary" type="submit" disabled={busy || !online}>
          Add player
        </button>
      </form>
      {!online && <p className="muted small">Start the server to change who can join.</p>}
      {msg && <Banner tone={msg.tone} title={msg.text}>{msg.hint}</Banner>}
      {list && list.length > 0 && (
        <div className="table-wrap" tabIndex={0}>
          <table>
            <thead>
              <tr>
                <th scope="col">Allowed player</th>
                <th scope="col">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((w) => (
                <tr key={w.name}>
                  <td>{w.name}</td>
                  <td className="num">
                    <button type="button" className="btn small ghost" onClick={() => remove(w.name)} disabled={!online} aria-label={`Remove ${w.name}`}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {list && list.length === 0 && <p className="muted small">Nobody is on the list yet.</p>}
    </div>
  )
}
