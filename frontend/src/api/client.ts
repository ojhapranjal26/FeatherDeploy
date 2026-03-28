import axios from 'axios'

const client = axios.create({
  baseURL: '/api',
  headers: { 'Content-Type': 'application/json' },
})

client.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

client.interceptors.response.use(
  (res) => res,
  (err) => {
    // Don't redirect on 401 for the login endpoint itself — let the form's
    // catch block show the "invalid credentials" toast instead.
    // Don't redirect for GitHub OAuth endpoints — a 401 there means the user's
    // GitHub token expired (github_token_expired), not their app session.
    const isLoginEndpoint = err.config?.url?.includes('/auth/login')
    const isGithubEndpoint = err.config?.url?.startsWith('/github/')
    if (err.response?.status === 401 && !isLoginEndpoint && !isGithubEndpoint) {
      localStorage.removeItem('token')
      window.location.href = '/login'
    }
    return Promise.reject(err)
  }
)

export default client
