import React from 'react'
import { AlertTriangle, RefreshCw } from 'lucide-react'

class ErrorBoundary extends React.Component {
  constructor(props) {
    super(props)
    this.state = { hasError: false, error: null }
  }

  static getDerivedStateFromError(error) {
    return { hasError: true, error }
  }

  componentDidCatch(error, errorInfo) {
    console.error('ErrorBoundary caught:', error, errorInfo)
  }

  handleReset = () => {
    this.setState({ hasError: false, error: null })
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback
      }

      return (
        <div className="flex flex-col items-center justify-center p-12 min-h-[200px] text-center">
          <div className="w-14 h-14 flex items-center justify-center mx-auto mb-5 border border-[var(--canvas-muted)]">
            <AlertTriangle className="w-6 h-6 text-danger" strokeWidth={1.5} />
          </div>
          <h3 className="font-serif text-h3 text-[var(--ink-charcoal)] mb-2">Bir sorun olustu</h3>
          <p className="text-meta text-text-secondary font-sans mb-6 max-w-md leading-[1.55]">
            Bu bolumde beklenmeyen bir hata olustu. Sayfayi yenileyerek tekrar deneyebilirsiniz.
          </p>
          <button
            onClick={this.handleReset}
            className="btn-secondary inline-flex items-center gap-2 text-meta"
          >
            <RefreshCw className="w-4 h-4" />
            Tekrar Dene
          </button>
        </div>
      )
    }

    return this.props.children
  }
}

export default ErrorBoundary
