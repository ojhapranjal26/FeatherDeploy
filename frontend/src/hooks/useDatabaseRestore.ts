import { useState } from 'react'
import { databasesApi } from '@/api/databases'
import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'

const CHUNK_SIZE = 5 * 1024 * 1024 // 5MB chunks

export function useDatabaseRestore(projectId: string | number, databaseId: string | number) {
  const [isUploading, setIsUploading] = useState(false)
  const [uploadProgress, setUploadProgress] = useState(0)
  const qc = useQueryClient()

  const restore = async (file: File) => {
    setIsUploading(true)
    setUploadProgress(0)

    try {
      // For small files (< 10MB), use the simple upload
      if (file.size < 10 * 1024 * 1024) {
        const res = await databasesApi.restoreBackup(projectId, databaseId, file)
        setIsUploading(false)
        return res
      }

      // For larger files, use multipart upload
      const { upload_id } = await databasesApi.restoreUploadInit(projectId, databaseId, file.name, file.size)
      
      const totalParts = Math.ceil(file.size / CHUNK_SIZE)
      
      for (let i = 0; i < totalParts; i++) {
        const start = i * CHUNK_SIZE
        const end = Math.min(start + CHUNK_SIZE, file.size)
        const chunk = file.slice(start, end)
        
        await databasesApi.restoreUploadPart(projectId, databaseId, upload_id, i, chunk)
        setUploadProgress(Math.round(((i + 1) / totalParts) * 100))
      }

      const res = await databasesApi.restoreUploadComplete(projectId, databaseId, upload_id)
      setIsUploading(false)
      return { status: 'queued', task_id: res.task_id }
    } catch (err: any) {
      setIsUploading(false)
      const msg = err?.response?.data?.error ?? err.message ?? 'Upload failed'
      toast.error(msg)
      throw err
    }
  }

  return { restore, isUploading, uploadProgress }
}
