import { useNavigate } from 'react-router-dom'

export default function HomePage() {
  const navigate = useNavigate()

  return (
    <main className="home-page">
      <section className="home-card">
        <p className="home-kicker">Gomoku</p>
        <h1>Welcome</h1>
        <p className="home-copy">Start a new game session from here.</p>
        <button type="button" onClick={() => navigate('/game')}>
          Go to game
        </button>
      </section>
    </main>
  )
}
